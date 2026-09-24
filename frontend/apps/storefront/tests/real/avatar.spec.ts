import { test, expect, type Page } from '@playwright/test';
import { createHash } from 'node:crypto';
import AxeBuilder from '@axe-core/playwright';
import { verifyEmail, submitLogin } from './mail';

const password = 'CorrectHorse9!';
async function login(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await submitLogin(page, email);
  await expect(page).toHaveURL(/\/account\/id$/);
  await expect(page.getByRole('button', { name: 'Изменить данные', exact: true })).toBeVisible();
  await expect(page.getByLabel('Изображение для аватара')).toBeEnabled();
  await expect(page.getByRole('button', { name: 'Обновить состояние', exact: true })).toBeEnabled();
}
async function register(page: Page, email: string) {
  await page.goto('/register');
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByLabel('Повторите пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Зарегистрироваться', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Аккаунт создан.' })).toBeVisible();
  await verifyEmail(page, email);
  await login(page, email);
}
async function rpc(page: Page, service: 'user' | 'files', method: string, body: object = {}) {
  return page.evaluate(
    async ({ service, method, body }) => {
      const r = await fetch(
        `/${service}.v1.${service === 'user' ? 'UserService' : 'FileService'}/${method}`,
        {
          method: 'POST',
          credentials: 'same-origin',
          cache: 'no-store',
          headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1' },
          body: JSON.stringify(body),
        },
      );
      return {
        status: r.status,
        cache: r.headers.get('cache-control'),
        body: (await r.json()) as {
          avatar?: { subjectId: string; version: string; fileId?: string };
          fileId?: string;
          state?: string;
        },
      };
    },
    { service, method, body },
  );
}
async function raster(page: Page, color: string) {
  const data = await page.evaluate((color) => {
    const c = document.createElement('canvas');
    c.width = 96;
    c.height = 96;
    const context = c.getContext('2d')!;
    context.fillStyle = color;
    context.fillRect(0, 0, 96, 96);
    return c.toDataURL('image/png').split(',')[1]!;
  }, color);
  return Buffer.from(data, 'base64');
}
async function upload(page: Page, bytes: Buffer) {
  await page
    .getByLabel('Изображение для аватара')
    .setInputFiles({ name: 'avatar.png', mimeType: 'image/png', buffer: bytes });
  await page.getByRole('button', { name: 'Загрузить аватар', exact: true }).click();
  await expect(page.getByText('Аватар сохранён.', { exact: true })).toBeVisible({
    timeout: 90_000,
  });
  await expect(page.getByAltText('Ваш сохранённый аватар', { exact: true })).toBeVisible();
}

test('real avatar uses direct verified Files bytes, owner isolation, CAS and durable retirement', async ({
  page,
  context,
  browser,
}) => {
  test.skip(process.env.ACCOUNT_AVATAR_E2E !== 'true' || process.env.ACCOUNT_E2E_PHASE !== 'core');
  test.setTimeout(240_000);
  const email = `avatar-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`;
  await register(page, email);
  const before = await rpc(page, 'user', 'GetAvatar');
  expect(before.status).toBe(200);
  expect(before.cache?.split(',').map((value) => value.trim())).toContain('no-store');
  const original = await raster(page, '#245b43');
  let directPut = 0;
  let directGet = 0;
  let storageCredentials = false;
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (
      [
        'https://quarantine:8333',
        'https://delivery-a:8333',
        'https://delivery-b:8333',
        'https://localhost:18343',
        'https://localhost:18344',
        'https://localhost:18345',
      ].includes(url.origin)
    ) {
      if (request.method() === 'PUT') directPut++;
      if (request.method() === 'GET') directGet++;
      const h = request.headers();
      storageCredentials ||= !!h.cookie || !!h.authorization || !!h.referer;
    }
  });
  await upload(page, original);
  const first = (await rpc(page, 'user', 'GetAvatar')).body.avatar!;
  expect(!!first.fileId).toBe(true);
  expect(first.version).toBe('2');
  expect(directPut).toBe(1);
  expect(directGet).toBeGreaterThan(0);
  expect(storageCredentials).toBe(false);
  expect((await new AxeBuilder({ page }).analyze()).violations.map((v) => v.id)).toEqual([]);
  await page.reload();
  await expect(page.getByAltText('Ваш сохранённый аватар', { exact: true })).toBeVisible();
  const storageClean = await page.evaluate(
    () =>
      !/X-Amz-|Signature|avatar\.png/.test(JSON.stringify({ ...localStorage, ...sessionStorage })),
  );
  expect(storageClean).toBe(true);
  const pending = await rpc(page, 'files', 'CreateUpload', {
    idempotencyKey: Buffer.alloc(16, 23).toString('base64'),
    mediaType: 'image/png',
    sizeBytes: String(original.length),
    sha256: createHash('sha256').update(original).digest('base64'),
    parts: [
      {
        sizeBytes: String(original.length),
        sha256: createHash('sha256').update(original).digest('base64'),
      },
    ],
  });
  expect(pending.status).toBe(200);
  expect(
    (
      await rpc(page, 'user', 'SetAvatar', {
        fileId: pending.body.fileId,
        expectedVersion: first.version,
      })
    ).status,
  ).toBe(400);
  expect((await rpc(page, 'user', 'GetAvatar')).body.avatar?.fileId === first.fileId).toBe(true);
  await rpc(page, 'files', 'Delete', { fileId: pending.body.fileId });
  const other = await browser.newContext();
  const foreign = await other.newPage();
  await register(foreign, `foreign-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`);
  expect((await rpc(foreign, 'files', 'GetStatus', { fileId: first.fileId })).status).toBe(404);
  expect(
    (await rpc(foreign, 'user', 'SetAvatar', { fileId: first.fileId, expectedVersion: '1' }))
      .status,
  ).toBe(400);
  expect((await rpc(foreign, 'user', 'GetAvatar')).body.avatar?.fileId ?? '').toBe('');
  await other.close();
  const secondTab = await context.newPage();
  await secondTab.goto('/account/id');
  await expect(
    secondTab.getByRole('button', { name: 'Удалить аватар', exact: true }),
  ).toBeEnabled();
  await upload(page, await raster(page, '#3a5577'));
  const second = (await rpc(page, 'user', 'GetAvatar')).body.avatar!;
  expect(second.version).toBe('3');
  expect(second.fileId !== first.fileId).toBe(true);
  await expect
    .poll(
      async () => (await rpc(page, 'files', 'GetStatus', { fileId: first.fileId })).body.state,
      { timeout: 20_000 },
    )
    .toBe('FILE_STATE_DELETED');
  await secondTab.getByRole('button', { name: 'Удалить аватар', exact: true }).click();
  await expect(secondTab.getByRole('alert')).toContainText('другой вкладке');
  await secondTab.getByRole('button', { name: 'Обновить состояние', exact: true }).click();
  await expect(secondTab.getByAltText('Ваш сохранённый аватар', { exact: true })).toBeVisible();
  await secondTab.close();
  await page.getByRole('button', { name: 'Выйти из аккаунта', exact: true }).click();
  await expect(page).toHaveURL(/\/login$/);
  await login(page, email);
  await expect(page.getByAltText('Ваш сохранённый аватар', { exact: true })).toBeVisible();
  let writes = 0;
  await page.route('**/user.v1.UserService/ClearAvatar', async (route) => {
    writes++;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    await route.abort('failed');
  });
  await page.getByRole('button', { name: 'Удалить аватар', exact: true }).click();
  await expect(page.getByText('Запись могла выполниться.', { exact: false })).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Результат запроса не подтверждён');
  expect(writes).toBe(1);
  await page.unroute('**/user.v1.UserService/ClearAvatar');
  await page.getByRole('button', { name: 'Обновить состояние', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Удалить аватар', exact: true })).toHaveCount(0);
  await expect(page.getByAltText('Ваш сохранённый аватар', { exact: true })).toHaveCount(0);
  await expect
    .poll(
      async () => (await rpc(page, 'files', 'GetStatus', { fileId: second.fileId })).body.state,
      { timeout: 20_000 },
    )
    .toBe('FILE_STATE_DELETED');
  const broken = Buffer.concat([
    Buffer.from('89504e470d0a1a0a', 'hex'),
    Buffer.from('not an image'),
  ]);
  await page
    .getByLabel('Изображение для аватара')
    .setInputFiles({ name: 'broken.png', mimeType: 'image/png', buffer: broken });
  await page.getByRole('button', { name: 'Загрузить аватар', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Файл пока недоступен', { timeout: 90_000 });
  await expect(page.getByAltText('Ваш сохранённый аватар', { exact: true })).toHaveCount(0);
  // Simulate access expiry while retaining the refresh cookie and in-memory candidate.
  await context.clearCookies({ name: '__Host-mm-access' });
  await page.getByRole('button', { name: 'Обновить состояние', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Отменить загрузку', exact: true })).toBeEnabled();
  await page.getByRole('button', { name: 'Отменить загрузку', exact: true }).click();
  await expect(page.getByText('Загрузка отменена.', { exact: true })).toBeVisible();
});

test('shared dev avatar survives restart and certificate renewal', async ({ page }) => {
  const mode = process.env.MM_DEV_PERSISTENCE;
  test.skip(!['seed', 'check'].includes(mode ?? ''));
  test.setTimeout(120_000);
  const email = `persistent-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`;
  if (mode === 'seed') {
    await register(page, email);
    await upload(page, await raster(page, 'green'));
  } else {
    await login(page, email);
  }
  const avatar = (await rpc(page, 'user', 'GetAvatar')).body.avatar!;
  expect(avatar.fileId).toBeTruthy();
  expect((await rpc(page, 'files', 'GetStatus', { fileId: avatar.fileId })).body.state).toBe(
    'FILE_STATE_READY',
  );
  const preview = page.getByAltText('Ваш сохранённый аватар', { exact: true });
  await expect(preview).toBeVisible();
  await expect
    .poll(() => preview.evaluate((image) => (image as HTMLImageElement).naturalWidth))
    .toBe(96);
});
