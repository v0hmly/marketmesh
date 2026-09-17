import { test, expect, type Page, type BrowserContext } from '@playwright/test';
import { fromBinary } from '@bufbuild/protobuf';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createServer } from 'node:https';

const run = process.env.ACCOUNT_E2E_RUN_ID!;
const account = (suffix: string) => ({
  identifier: `mm64-${run}-${suffix}`,
  password: `Mm64!${createHash('sha256').update(`${run}:${suffix}`).digest('hex')}`,
});
const a = account('a');
const b = account('b');
type AccountStage = 'profile' | 'book-initial' | 'book-relogin' | 'book-foreign' | 'maximum';
function step(
  stage: AccountStage,
  action: 'register' | 'login' | 'ready' | 'logout',
  phase: 'start' | 'done',
) {
  test
    .info()
    .annotations.push({ type: 'account-step', description: `${stage}:${action}:${phase}` });
}
const observedPages = new WeakSet<Page>();
function observeRpc(page: Page) {
  if (observedPages.has(page)) return;
  observedPages.add(page);
  const info = test.info();
  page.on('response', (response) => {
    const path = new URL(response.url()).pathname;
    const match =
      /^\/(?:auth\.v1\.AuthService\/(RegisterCredentials|Login|RefreshSession|Logout|LogoutAll)|user\.v1\.UserService\/(GetMe|UpdateMe|ListAddresses|CreateAddress|UpdateAddress|DeleteAddress|SetDefaultAddress))$/.exec(
        path,
      );
    if (match && info.annotations.filter((item) => item.type === 'account-rpc').length < 80)
      info.annotations.push({
        type: 'account-rpc',
        description: `${match[1] ?? match[2]}:${response.status()}`,
      });
  });
}
async function credentials(page: Page, who: typeof a) {
  await page.getByLabel('Логин', { exact: true }).fill(who.identifier);
  await page.getByLabel('Пароль', { exact: true }).fill(who.password);
}
async function register(page: Page, who: typeof a, stage: AccountStage = 'profile') {
  observeRpc(page);
  step(stage, 'register', 'start');
  await page.goto('/register');
  await credentials(page, who);
  await page.getByRole('button', { name: 'Создать аккаунт', exact: true }).click();
  await expect(
    page.getByText('Запрос обработан. Теперь войдите с вашим логином и паролем.'),
  ).toBeVisible();
  step(stage, 'register', 'done');
}
async function login(page: Page, who: typeof a, stage: AccountStage = 'profile') {
  observeRpc(page);
  step(stage, 'login', 'start');
  await page.goto('/login');
  await credentials(page, who);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  step(stage, 'login', 'done');
}
async function ready(page: Page, stage: AccountStage = 'profile') {
  step(stage, 'ready', 'start');
  await expect(page.getByLabel('Имя', { exact: true })).toBeVisible({ timeout: 45_000 });
  step(stage, 'ready', 'done');
}
async function confirmedLogout(page: Page, stage: AccountStage) {
  step(stage, 'logout', 'start');
  const response = page.waitForResponse(
    (reply) => new URL(reply.url()).pathname === '/auth.v1.AuthService/Logout',
  );
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  expect((await response).status()).toBe(200);
  // App navigates only after the durable session journal is settled.
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByLabel('Логин', { exact: true })).toBeEnabled();
  step(stage, 'logout', 'done');
}
async function rpc(page: Page, method = 'GetMe', body: Record<string, unknown> = {}) {
  return page.evaluate(
    async ({ method, body }) => {
      const response = await fetch(`/user.v1.UserService/${method}`, {
        method: 'POST',
        credentials: 'same-origin',
        cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1' },
        body: JSON.stringify(body),
      });
      return {
        status: response.status,
        cache: response.headers.get('cache-control'),
        body: (await response.json()) as {
          details?: { type: string; value: string }[];
          book?: {
            subjectId: string;
            version: string;
            addresses?: {
              addressId: string;
              isDefault?: boolean;
              fields: Record<string, string>;
            }[];
          };
          profile?: { subjectId: string; version: string; displayName: string; bio: string };
        },
      };
    },
    { method, body },
  );
}
async function save(page: Page, name: string, bio: string) {
  await page.getByLabel('Имя', { exact: true }).fill(name);
  await page.getByRole('textbox', { name: 'О себе', exact: true }).fill(bio);
  await page.getByRole('button', { name: 'Сохранить изменения', exact: true }).click();
  await expect(page.getByText('Изменения сохранены.', { exact: true })).toBeVisible();
}
async function assertPrivateCookies(context: BrowserContext, page: Page) {
  const cookies = await context.cookies();
  const session = cookies.filter((cookie) => cookie.name.startsWith('__Host-mm-'));
  expect(session.length).toBe(2);
  expect(
    session.every(
      (cookie) =>
        cookie.httpOnly &&
        cookie.secure &&
        cookie.sameSite === 'Strict' &&
        cookie.path === '/' &&
        cookie.domain === new URL(process.env.BASE_URL!).hostname,
    ),
  ).toBe(true);
  const secrets = [
    a.identifier,
    a.password,
    b.identifier,
    b.password,
    ...session.map((cookie) => cookie.value),
  ];
  expect(
    await page.evaluate((forbidden) => {
      const stored = JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } });
      return (
        !document.cookie.includes('__Host-mm-') &&
        forbidden.every((secret) => !stored.includes(secret)) &&
        !/access_token|refresh_token|password/i.test(stored)
      );
    }, secrets),
  ).toBe(true);
}

test('Chromium rejects an unrelated self-signed certificate', async ({ page }) => {
  const directory = mkdtempSync(join(tmpdir(), 'mm64-untrusted-'));
  const key = join(directory, 'key.pem');
  const cert = join(directory, 'cert.pem');
  execFileSync(
    'openssl',
    [
      'req',
      '-x509',
      '-newkey',
      'rsa:2048',
      '-nodes',
      '-days',
      '1',
      '-subj',
      '/CN=localhost',
      '-addext',
      'subjectAltName=DNS:localhost,IP:127.0.0.1',
      '-keyout',
      key,
      '-out',
      cert,
    ],
    { stdio: 'ignore' },
  );
  const server = createServer(
    { key: readFileSync(key), cert: readFileSync(cert) },
    (_request, response) => response.end('untrusted'),
  );
  try {
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    const address = server.address();
    if (!address || typeof address === 'string') throw new Error('Missing probe address');
    let rejected = false;
    try {
      await page.goto(`https://127.0.0.1:${address.port}`);
    } catch (error) {
      rejected = error instanceof Error && error.message.includes('ERR_CERT_AUTHORITY_INVALID');
    }
    expect(rejected).toBe(true);
    let requestRejected = false;
    try {
      await page.request.get(`https://127.0.0.1:${address.port}`);
    } catch (error) {
      requestRejected = error instanceof Error && /self[- ]signed certificate/i.test(error.message);
    }
    expect(requestRejected).toBe(true);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    rmSync(directory, { recursive: true, force: true });
  }
});

test('registration traverses Auth and leaves a real pending User projection', async ({ page }) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'pending');
  await register(page, a);
  await login(page, a);
  await expect(page.getByRole('heading', { name: 'Готовим ваш профиль' })).toBeVisible();
  const response = await rpc(page);
  expect(response.status).toBe(404);
  expect(response.cache).toContain('no-store');
  const detail = response.body.details?.find((item) => item.type === 'google.rpc.ErrorInfo');
  expect(Boolean(detail)).toBe(true);
  const info = fromBinary(ErrorInfoSchema, Buffer.from(detail!.value, 'base64'));
  expect(info.domain).toBe('marketmesh.user');
  expect(info.reason).toBe('PROFILE_NOT_READY');
});

test('real profile, CAS, isolated owners, cookie security and revocation', async ({
  browser,
  page,
  context,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  await login(page, a);
  await ready(page);
  await save(page, 'Мастер А', '<script>window.unwanted = true</script>');
  await page.reload();
  await ready(page);
  await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveValue(
    '<script>window.unwanted = true</script>',
  );
  expect(await page.evaluate(() => 'unwanted' in window)).toBe(false);
  const original = await rpc(page);
  expect(original.status).toBe(200);
  expect(original.cache).toContain('no-store');
  await assertPrivateCookies(context, page);
  const second = await context.newPage();
  await second.goto('/account');
  await ready(second);
  await save(page, 'Мастер А', 'Сохранено первой вкладкой');
  await second.getByRole('textbox', { name: 'О себе', exact: true }).fill('Мой черновик');
  await second.getByRole('button', { name: 'Сохранить изменения', exact: true }).click();
  await expect(second.getByRole('button', { name: 'Перечитать актуальные данные' })).toBeVisible();
  await expect(second.getByRole('textbox', { name: 'О себе', exact: true })).toHaveValue(
    'Мой черновик',
  );
  await second.getByRole('button', { name: 'Перечитать актуальные данные' }).click();
  await expect(second.locator('.latest-profile')).toContainText('Сохранено первой вкладкой');
  await second.getByRole('button', { name: 'Оставить мой черновик для сохранения' }).click();
  await second.getByRole('button', { name: 'Сохранить изменения', exact: true }).click();
  await expect(second.getByText('Изменения сохранены.', { exact: true })).toBeVisible();
  await second.close();
  const other = await browser.newContext({
    baseURL: process.env.BASE_URL,
    ignoreHTTPSErrors: false,
  });
  const otherPage = await other.newPage();
  await register(otherPage, b);
  await login(otherPage, b);
  await ready(otherPage);
  await save(otherPage, 'Мастер Б', 'Изолированный профиль');
  const profileB = await rpc(otherPage);
  expect(profileB.status).toBe(200);
  expect(profileB.body.profile?.subjectId !== original.body.profile?.subjectId).toBe(true);
  const forged = await rpc(otherPage, 'GetMe', { subjectId: original.body.profile?.subjectId });
  expect(
    forged.status === 400 ||
      (forged.status === 200 &&
        forged.body.profile?.subjectId === profileB.body.profile?.subjectId),
  ).toBe(true);
  const forgedWrite = await rpc(otherPage, 'UpdateMe', {
    subjectId: original.body.profile?.subjectId,
    displayName: 'Мастер Б',
    bio: 'Изолированный профиль',
    expectedVersion: profileB.body.profile?.version,
  });
  expect(
    forgedWrite.status === 400 ||
      (forgedWrite.status === 200 &&
        forgedWrite.body.profile?.subjectId === profileB.body.profile?.subjectId),
  ).toBe(true);
  expect((await rpc(page)).body.profile?.bio).toBe('Мой черновик');
  await assertPrivateCookies(other, otherPage);
  const anonymous = await browser.newContext({
    baseURL: process.env.BASE_URL,
    ignoreHTTPSErrors: false,
  });
  const anonymousPage = await anonymous.newPage();
  await anonymousPage.goto('/robots.txt');
  expect((await rpc(anonymousPage)).status).toBe(401);
  await anonymous.close();
  const stolen = await context.cookies();
  const logoutResponse = page.waitForResponse((response) =>
    response.url().endsWith('/auth.v1.AuthService/Logout'),
  );
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  expect((await logoutResponse).status()).toBe(200);
  await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveCount(0);
  // A separate context retains the old cookies; no UI bootstrap runs in this page.
  const replay = await browser.newContext({
    baseURL: process.env.BASE_URL,
    ignoreHTTPSErrors: false,
  });
  await replay.addCookies(stolen);
  const replayPage = await replay.newPage();
  await replayPage.goto('/robots.txt');
  expect((await rpc(replayPage)).status).toBe(401);
  await login(page, a);
  await ready(page);
  const parallel = await browser.newContext({
    baseURL: process.env.BASE_URL,
    ignoreHTTPSErrors: false,
  });
  const parallelPage = await parallel.newPage();
  await login(parallelPage, a);
  await ready(parallelPage);
  const logoutAllResponse = page.waitForResponse((response) =>
    response.url().endsWith('/auth.v1.AuthService/LogoutAll'),
  );
  await page.getByRole('button', { name: 'Выйти на всех устройствах', exact: true }).click();
  expect((await logoutAllResponse).status()).toBe(200);
  await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveCount(0);
  expect((await rpc(parallelPage)).status).toBe(401);
  expect((await rpc(otherPage)).status).toBe(200);
  await other.setOffline(true);
  await otherPage.getByRole('button', { name: 'Выйти', exact: true }).click();
  await expect(
    otherPage.getByText(
      'Сервер не подтвердил выход. Сессия может оставаться активной. Проверьте соединение.',
    ),
  ).toBeVisible();
  await expect(otherPage.getByRole('textbox', { name: 'О себе', exact: true })).toHaveCount(0);
  await other.setOffline(false);
  // An unconfirmed local logout must not claim server revocation.
  expect((await rpc(otherPage)).status).toBe(200);
  await Promise.all([other.close(), replay.close(), parallel.close()]);
});

const bookA = account('book-a');
const bookB = account('book-b');
const delivery = (recipient = 'Получатель А') => ({
  recipient,
  phone: '+7 (999) 123-45-67',
  country: 'Россия',
  postalCode: '123456',
  city: 'Москва',
  streetHouse: 'Улица Мира, дом 1',
  apartment: '2',
  comment: 'Позвоните перед доставкой',
});
async function openAddresses(page: Page) {
  await page.getByRole('link', { name: 'Адреса доставки', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Добавить адрес', exact: true })).toBeVisible();
}
async function createDelivery(page: Page, recipient: string) {
  await page.getByRole('button', { name: 'Добавить адрес', exact: true }).click();
  for (const [key, value] of Object.entries(delivery(recipient)))
    await page.locator(`#address-${key}`).fill(value);
  await page.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
}
function errorInfo(response: Awaited<ReturnType<typeof rpc>>) {
  const detail = response.body.details?.find((item) => item.type === 'google.rpc.ErrorInfo');
  expect(Boolean(detail)).toBe(true);
  return fromBinary(ErrorInfoSchema, Buffer.from(detail!.value, 'base64'));
}

test('real address book CRUD, CAS, owner isolation, relogin and ambiguous operations', async ({
  page,
  context,
  browser,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  await register(page, bookA, 'book-initial');
  await login(page, bookA, 'book-initial');
  await ready(page, 'book-initial');
  await openAddresses(page);
  await createDelivery(page, 'Первый получатель');
  await expect(page.locator('.address-card')).toHaveCount(1);
  await createDelivery(page, 'Второй получатель');
  await expect(page.locator('.address-card')).toHaveCount(2);
  let response = await rpc(page, 'ListAddresses');
  expect(response.status).toBe(200);
  expect(response.cache).toContain('no-store');
  expect(response.body.book?.addresses?.length).toBe(2);
  expect(response.body.book?.addresses?.filter((item) => item.isDefault).length).toBe(1);
  const firstId = response.body.book!.addresses![0]!.addressId;
  const second = page.locator('.address-card').filter({ hasText: 'Второй получатель' });
  await second.getByRole('button', { name: 'Использовать по умолчанию', exact: false }).click();
  await expect(second).toContainText('По умолчанию');
  const otherTab = await context.newPage();
  await otherTab.goto('/account/addresses');
  await expect(otherTab.locator('.address-card')).toHaveCount(2);
  await otherTab
    .locator('.address-card')
    .filter({ hasText: 'Первый получатель' })
    .getByRole('button', { name: 'Изменить', exact: false })
    .click();
  await otherTab.getByLabel('Комментарий', { exact: true }).fill('Черновик второй вкладки');
  await second.getByRole('button', { name: 'Изменить', exact: false }).click();
  await page.getByLabel('Комментарий', { exact: true }).fill('Сохранено первой вкладкой');
  await page.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
  await expect(page.getByText('Адрес сохранён.', { exact: true })).toBeVisible();
  await otherTab.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
  await expect(
    otherTab.getByRole('button', { name: 'Перечитать актуальные данные' }),
  ).toBeVisible();
  await expect(otherTab.getByLabel('Комментарий', { exact: true })).toHaveValue(
    'Черновик второй вкладки',
  );
  await otherTab.getByRole('button', { name: 'Перечитать актуальные данные' }).click();
  await expect(
    otherTab.locator('.address-card').filter({ hasText: 'Второй получатель' }),
  ).toContainText('Сохранено первой вкладкой');
  await otherTab.getByRole('button', { name: 'Оставить мой черновик для сохранения' }).click();
  await otherTab.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
  await expect(otherTab.getByText('Адрес сохранён.', { exact: true })).toBeVisible();
  await otherTab.close();
  await page.reload();
  await expect(page.locator('.address-card')).toHaveCount(2);
  await second.getByRole('button', { name: 'Удалить', exact: false }).click();
  await expect(page.getByRole('button', { name: 'Отменить удаление' })).toBeVisible();
  expect((await rpc(page, 'ListAddresses')).body.book?.addresses?.length).toBe(2);
  await page.getByRole('button', { name: 'Подтвердить удаление' }).click();
  await expect(page.locator('.address-card')).toHaveCount(1);
  await expect(page.getByText('По умолчанию', { exact: true })).toHaveCount(0);
  await confirmedLogout(page, 'book-relogin');
  await login(page, bookA, 'book-relogin');
  await ready(page, 'book-relogin');
  await openAddresses(page);
  await expect(page.locator('.address-card')).toHaveCount(1);
  await expect(page.locator('.address-card')).toContainText('Черновик второй вкладки');
  const other = await browser.newContext({
    baseURL: process.env.BASE_URL,
    ignoreHTTPSErrors: false,
  });
  try {
    const foreign = await other.newPage();
    await register(foreign, bookB, 'book-foreign');
    await login(foreign, bookB, 'book-foreign');
    await ready(foreign, 'book-foreign');
    await openAddresses(foreign);
    await expect(foreign.locator('.address-card')).toHaveCount(0);
    const theirs = await rpc(foreign, 'ListAddresses');
    response = await rpc(page, 'ListAddresses');
    expect(theirs.body.book?.subjectId !== response.body.book?.subjectId).toBe(true);
    const missingId = Buffer.alloc(16, 239).toString('base64');
    for (const method of ['UpdateAddress', 'DeleteAddress', 'SetDefaultAddress']) {
      const forged = await rpc(foreign, method, {
        addressId: firstId,
        expectedBookVersion: theirs.body.book!.version,
        ...(method === 'UpdateAddress' ? { fields: delivery() } : {}),
      });
      const missing = await rpc(foreign, method, {
        addressId: missingId,
        expectedBookVersion: theirs.body.book!.version,
        ...(method === 'UpdateAddress' ? { fields: delivery() } : {}),
      });
      expect(forged.status).toBe(404);
      expect(missing.status).toBe(404);
      expect(errorInfo(forged).reason).toBe('ADDRESS_NOT_FOUND');
      expect(errorInfo(forged).domain).toBe('marketmesh.user');
      expect(errorInfo(missing).reason).toBe('ADDRESS_NOT_FOUND');
    }
    expect((await rpc(page, 'ListAddresses')).body.book?.addresses?.length).toBe(1);
  } finally {
    await other.close();
  }
  // The write reaches the real service; only its reply is lost at the browser boundary.
  let creates = 0;
  await page.route('**/user.v1.UserService/CreateAddress', async (route) => {
    creates++;
    const committed = await route.fetch();
    expect(committed.status()).toBe(200);
    await route.abort('failed');
  });
  await createDelivery(page, 'Подтверждение потеряно');
  await expect(page.getByText('Новый адрес мог уже сохраниться.', { exact: false })).toBeVisible();
  expect(creates).toBe(1);
  await page.unroute('**/user.v1.UserService/CreateAddress');
  await page.getByRole('button', { name: 'Перечитать актуальные данные' }).click();
  await expect(page.locator('.address-card')).toHaveCount(2);
  await page.getByRole('button', { name: 'Принять актуальную книгу' }).click();
  expect(creates).toBe(1);
  const privateValues = [
    'Первый получатель',
    'Подтверждение потеряно',
    'Улица Мира',
    bookA.identifier,
    bookA.password,
  ];
  expect(
    await page.evaluate((values) => {
      const stored = JSON.stringify({ ...localStorage, ...sessionStorage });
      return values.every((value) => !stored.includes(value));
    }, privateValues),
  ).toBe(true);
  let logouts = 0;
  await page.route('**/auth.v1.AuthService/Logout', async (route) => {
    logouts++;
    const revoked = await route.fetch();
    expect(revoked.status()).toBe(200);
    await route.abort('failed');
  });
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  await expect(page.locator('.address-card')).toHaveCount(0);
  await expect(page.getByText('Сервер не подтвердил выход.', { exact: false })).toBeVisible();
  expect(logouts).toBe(1);
  await page.unroute('**/auth.v1.AuthService/Logout');
  await page.reload();
  await expect(
    page.getByText('Результат операции с сессией неизвестен.', { exact: false }),
  ).toBeVisible();
  expect(logouts).toBe(1);
});

test('real maximum Unicode address book crosses 64 KiB and enforces the twenty address limit', async ({
  page,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  const maximum = account('book-max');
  await register(page, maximum, 'maximum');
  await login(page, maximum, 'maximum');
  await ready(page, 'maximum');
  const fields = {
    recipient: '🪡'.repeat(120),
    phone: '+7 999 1234567',
    country: '🪡'.repeat(80),
    postalCode: '🪡'.repeat(20),
    city: '🪡'.repeat(120),
    streetHouse: '🪡'.repeat(240),
    apartment: '🪡'.repeat(40),
    comment: '🪡'.repeat(500),
  };
  let current = await rpc(page, 'ListAddresses');
  for (let index = 0; index < 20; index++) {
    current = await rpc(page, 'CreateAddress', {
      fields,
      expectedBookVersion: current.body.book!.version,
    });
    expect(current.status).toBe(200);
  }
  expect(current.body.book?.addresses?.length).toBe(20);
  const limit = await rpc(page, 'CreateAddress', {
    fields,
    expectedBookVersion: current.body.book!.version,
  });
  expect(limit.status).toBe(429);
  expect(errorInfo(limit).reason).toBe('ADDRESS_LIMIT_REACHED');
  await openAddresses(page);
  await expect(page.locator('.address-card')).toHaveCount(20);
  await expect(page.getByRole('button', { name: 'Добавить адрес', exact: true })).toBeDisabled();
  // Exercise the actual browser's binary full-book response and the larger transport limit.
  const size = await page.evaluate(async () => {
    const response = await fetch('/user.v1.UserService/ListAddresses', {
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { 'Content-Type': 'application/proto', 'Connect-Protocol-Version': '1' },
      body: new Uint8Array(),
    });
    return { status: response.status, bytes: (await response.arrayBuffer()).byteLength };
  });
  expect(size.status).toBe(200);
  expect(size.bytes > 65_536 && size.bytes < 131_072).toBe(true);
  await page.reload();
  await expect(page.locator('.address-card')).toHaveCount(20);
});
