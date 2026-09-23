// Protocol check against the existing dev NATS account, with the Auth identity.
import { readFileSync } from 'node:fs';
import net from 'node:net';
import tls from 'node:tls';

const timer = setTimeout(() => { console.error('NATS isolation timeout'); process.exit(1); }, 10000);
const raw = net.connect(4222, 'nats');
raw.on('error', () => { console.error('NATS connection failed'); process.exit(1); });
raw.once('data', (info) => {
  if (!info.toString().startsWith('INFO ')) process.exit(1);
  const socket = tls.connect({
    socket: raw, servername: 'nats', minVersion: 'TLSv1.3',
    ca: readFileSync('/certs/ca.pem'), cert: readFileSync('/certs/cert.pem'), key: readFileSync('/certs/key.pem'),
  }, () => socket.write('CONNECT {"verbose":true,"tls_required":true}\r\nSUB _INBOX.mm99 1\r\nPING\r\n'));
  let stage = 0;
  let output = '';
  socket.on('error', () => { console.error('NATS TLS failed'); process.exit(1); });
  socket.on('data', (data) => {
    output += data.toString();
    if (!output.includes('PONG\r\n')) return;
    if (stage === 0) {
      if (output.includes('-ERR')) process.exit(1);
      stage = 1;
      output = '';
      socket.write('SUB private.> 2\r\nPUB private.mm99 1\r\nx\r\nPUB $JS.API.STREAM.INFO.OTHER 0\r\n\r\nPING\r\n');
    } else {
      if ((output.match(/Permissions Violation/g) || []).length !== 3) process.exit(1);
      clearTimeout(timer);
      socket.end();
      console.log('NATS: mTLS identity, allowed inbox and denied foreign subject/stream verified');
    }
  });
});
