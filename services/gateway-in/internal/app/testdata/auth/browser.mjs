import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { isAbsolute } from 'node:path';

let stage = 'configuration';
let chrome;
let socket;
let nextID = 0;
const pending = new Map();
let session;
const ensure = (condition) => { if (!condition) throw new Error('check failed'); };
const deadline = setTimeout(() => {
  process.stderr.write(`Browser Auth check failed: ${stage} timeout\n`);
  cleanup().finally(() => process.exit(1));
}, 30000);

async function cleanup() {
  clearTimeout(deadline);
  if (socket?.readyState === WebSocket.OPEN) {
    try { socket.send(JSON.stringify({ id: ++nextID, method: 'Browser.close' })); } catch {}
    socket.close();
  }
  if (chrome?.pid && chrome.exitCode === null && chrome.signalCode === null) {
    chrome.kill('SIGTERM');
    const kill = setTimeout(() => chrome.kill('SIGKILL'), 1000);
    try { await once(chrome, 'exit'); } catch {}
    clearTimeout(kill);
  }
}
function call(method, params = {}, target = session) {
  const id = ++nextID;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params, ...(target ? { sessionId: target } : {}) }));
  });
}
async function evaluate(expression) {
  const result = await call('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
  ensure(!result.exceptionDetails);
  return result.result.value;
}
async function cookies(origin) {
  return (await call('Network.getCookies', { urls: [origin] })).cookies;
}
async function mutation(method, body = {}) {
  return evaluate(`(async () => {
    const r = await fetch('/auth.v1.AuthService/${method}', {
      method: 'POST', credentials: 'same-origin',
      headers: {'Content-Type':'application/json','Connect-Protocol-Version':'1'},
      body: JSON.stringify(${JSON.stringify(body)})
    });
    const text = await r.text();
    let body; try {body = JSON.parse(text)} catch {return {valid:false}};
    return {valid:true,ok:r.ok,hidden:r.headers.get('set-cookie')===null,
      noStore:r.headers.get('cache-control')==='no-store',body};
  })()`);
}
function checkResponse(result, ok = true) {
  ensure(result.valid && result.ok === ok && result.hidden && result.noStore);
  if (ok) ensure(Object.keys(result.body).every(key => key === 'subjectId'));
}
function checkCookies(values, host) {
  ensure(values.length === 2);
  ensure(values.some(c => c.name === '__Host-mm-access') && values.some(c => c.name === '__Host-mm-refresh'));
  ensure(values.every(c => c.value.length > 0 && c.expires > Date.now()/1000));
  ensure(values.every(c => c.httpOnly && c.secure && c.sameSite === 'Strict' && c.path === '/' && c.domain === host));
}

try {
  let input = '';
  for await (const chunk of process.stdin) { input += chunk; ensure(input.length <= 16384); }
  const { origin, spki, chrome: binary, profile } = JSON.parse(input);
  const url = new URL(origin);
  ensure(url.protocol === 'https:' && ['localhost','127.0.0.1'].includes(url.hostname));
  ensure(url.origin === origin && !url.username && !url.password);
  ensure(typeof spki === 'string' && /^[A-Za-z0-9+/]{43}=$/.test(spki));
  ensure(typeof binary === 'string' && isAbsolute(binary) && typeof profile === 'string' && isAbsolute(profile));
  stage = 'browser startup';
  chrome = spawn(binary, ['--headless','--disable-gpu','--no-first-run','--no-default-browser-check',
    '--disable-background-networking','--disable-component-update','--disable-sync','--disable-extensions','--disable-default-apps',
    `--user-data-dir=${profile}`, '--remote-debugging-port=0','--remote-debugging-address=127.0.0.1',
    `--ignore-certificate-errors-spki-list=${spki}`, 'about:blank'], { stdio: ['ignore','ignore','pipe'] });
  const endpoint = await new Promise((resolve, reject) => {
    let buffer = '';
    chrome.once('error', reject);
    chrome.once('exit', () => reject(new Error('browser stopped')));
    chrome.stderr.on('data', chunk => {
      buffer = (buffer + chunk.toString()).slice(-16384);
      const match = buffer.match(/DevTools listening on (ws:\/\/127\.0\.0\.1:\d+\/devtools\/browser\/[a-zA-Z0-9-]+)/);
      if (match) resolve(match[1]);
    });
  });
  socket = new WebSocket(endpoint);
  socket.addEventListener('message', event => {
    let response; try { response = JSON.parse(event.data); } catch { return; }
    const handler = pending.get(response.id);
    if (handler) { pending.delete(response.id); response.error ? handler.reject(new Error('browser command failed')) : handler.resolve(response.result); }
  });
  await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}); });
  const target = await call('Target.createTarget', { url: 'about:blank' }, null);
  session = (await call('Target.attachToTarget', {targetId: target.targetId, flatten:true}, null)).sessionId;
  await call('Page.enable');
  await call('Runtime.enable');
  await call('Network.enable');
  stage = 'HTTPS navigation';
  await call('Page.navigate', {url: origin+'/browser-probe'});
  for (;;) {
    if (await evaluate(`location.origin === ${JSON.stringify(origin)} && document.readyState !== 'loading'`)) break;
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  ensure(await evaluate('window.isSecureContext'));
  stage = 'registration';
  const credentials = { identifier: `browser-${crypto.randomUUID()}@example.test`, password: Buffer.from('Browser-test-secret-42!').toString('base64') };
  checkResponse(await mutation('RegisterCredentials', credentials));
  stage = 'login';
  checkResponse(await mutation('Login', credentials));
  const first = await cookies(origin);
  checkCookies(first, url.hostname);
  ensure(await evaluate('document.cookie === ""'));
  stage = 'refresh';
  checkResponse(await mutation('RefreshSession'));
  const rotated = await cookies(origin);
  checkCookies(rotated, url.hostname);
  ensure(rotated.every(c => first.some(old => old.name === c.name && old.value !== c.value)));
  ensure(await evaluate('document.cookie === ""'));
  stage = 'logout';
  checkResponse(await mutation('Logout'));
  ensure((await cookies(origin)).length === 0);
  checkResponse(await mutation('RefreshSession'), false);
  stage = 'logout all';
  checkResponse(await mutation('Login', credentials));
  checkCookies(await cookies(origin), url.hostname);
  checkResponse(await mutation('LogoutAll'));
  ensure((await cookies(origin)).length === 0);
  checkResponse(await mutation('RefreshSession'), false);
  await cleanup();
  process.stdout.write('Browser Auth HTTPS checks passed\n');
} catch {
  process.stderr.write(`Browser Auth check failed: ${stage}\n`);
  await cleanup();
  process.exitCode = 1;
}
