import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';

// Disposable certificate for UI-only tests. Full gateway E2E verifies its own CA.
const directory = mkdtempSync(join(tmpdir(), 'marketmesh-ui-test-'));
try {
  const cert = join(directory, 'cert.pem');
  const key = join(directory, 'key.pem');
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
      '-keyout',
      key,
      '-out',
      cert,
    ],
    { stdio: 'ignore' },
  );
  const result = spawnSync('pnpm', ['exec', 'playwright', 'test', ...process.argv.slice(2)], {
    stdio: 'inherit',
    env: { ...process.env, STOREFRONT_TLS_CERT: cert, STOREFRONT_TLS_KEY: key },
  });
  if (result.error) throw result.error;
  process.exitCode = result.status ?? 1;
} finally {
  rmSync(directory, { recursive: true, force: true });
}
