import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
const manifest = JSON.parse(
  readFileSync(new URL('../../dist/.vite/manifest.json', import.meta.url), 'utf8'),
) as Record<string, { file: string }>;
test.beforeEach(async ({ context }) => {
  await context.route(/\/(auth|user)\.v1\./, (route) =>
    route.fulfill({
      status: 401,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'unauthenticated', message: 'Sign in' }),
    }),
  );
});
test('buyer entry loads no seller screens or staff code', async ({ page }) => {
  const requests: string[] = [];
  page.on('request', (request) => requests.push(request.url()));
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: 'Рады вас видеть.' })).toBeVisible();
  const seller = Object.entries(manifest)
    .filter(([source]) => source.includes('/seller/'))
    .map(([, chunk]) => chunk.file);
  expect(seller.length).toBeGreaterThan(0);
  expect(requests.filter((url) => seller.some((file) => url.endsWith('/' + file)))).toEqual([]);
  expect(Object.keys(manifest).filter((source) => source.includes('/staff/'))).toEqual([]);
  await page.goto('/seller/login');
  await expect(page.getByRole('heading', { name: 'Войдите, чтобы вести магазин.' })).toBeVisible();
  expect(requests.some((url) => seller.some((file) => url.endsWith('/' + file)))).toBe(true);
});
test('a stale direct URL with a fragment reloads only after explicit recovery', async ({
  page,
  context,
}) => {
  let fail = true;
  let documents = 0;
  page.on('request', (request) => {
    if (request.isNavigationRequest() && request.frame() === page.mainFrame()) documents++;
  });
  const chunk = manifest['src/modules/account/profile/IdView.vue']!.file;
  await context.route('**/' + chunk, (route) =>
    fail ? route.fulfill({ status: 404, body: 'old chunk removed' }) : route.continue(),
  );
  await page.goto('/account/id#security');
  await expect(page.getByRole('heading', { name: 'Не удалось открыть раздел.' })).toBeVisible();
  expect(documents).toBe(1);
  fail = false;
  await page.getByRole('button', { name: 'Обновить страницу' }).click();
  await expect(page.getByRole('heading', { name: 'Не удалось открыть раздел.' })).not.toBeVisible();
  await expect(page).toHaveURL(/\/account\/id#security$/);
  expect(documents).toBe(2);
});

test('direct seller entry includes the shared authentication form styles', async ({ page }) => {
  await page.goto('/seller/apply');
  await page.getByLabel('Пароль', { exact: true }).fill('test-only-password');
  await expect(page.locator('.password-requirements')).toHaveCSS('display', 'flex');
  await expect(page.locator('.password-requirements')).toHaveCSS('flex-direction', 'column');
  await page.goto('/seller/login');
  // The code step is gated by an API challenge; inspect loaded CSS rules without
  // importing buyer UI first or weakening that gate in the production app.
  expect(
    await page.evaluate(() =>
      Array.from(document.styleSheets)
        .flatMap((sheet) => Array.from(sheet.cssRules))
        .some((rule) => rule.cssText.includes('.auth-code-actions')),
    ),
  ).toBe(true);
});
