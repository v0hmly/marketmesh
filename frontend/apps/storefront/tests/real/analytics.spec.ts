import { test, expect } from '@playwright/test';
import { createHash } from 'node:crypto';

test('analytics sends bounded fields once per page and an outage does not block login', async ({
  page,
}) => {
  const enabled = process.env.ACCOUNT_E2E_ANALYTICS === 'true';
  const bodies: string[] = [];
  let privateHeader = false;
  page.on('request', (request) => {
    if (new URL(request.url()).pathname !== '/analytics/track') return;
    bodies.push(request.postData() ?? '');
    const headers = request.headers();
    privateHeader ||= Boolean(headers.cookie || headers.authorization || headers.referer);
  });
  const queryCanary = 'private-query-canary';
  const received = enabled
    ? page.waitForResponse((response) => new URL(response.url()).pathname === '/analytics/track')
    : undefined;
  await page.goto(`/login?canary=${queryCanary}#private-fragment`);
  if (received) expect((await received).status()).toBe(204);
  await expect(page.getByRole('button', { name: 'Войти', exact: true })).toBeEnabled();
  if (!enabled) {
    await page.waitForTimeout(300);
    expect(bodies).toHaveLength(0);
    return;
  }
  await expect.poll(() => bodies.length).toBe(1);
  await page.evaluate(() => history.pushState({}, '', '/login?different=private#changed'));
  await page.waitForTimeout(200);
  expect(bodies).toHaveLength(1);
  expect(JSON.parse(bodies[0]!)).toEqual({ type: 'pageview', pathname: '/login' });

  const run = `${process.env.ACCOUNT_E2E_RUN_ID}-${process.env.ACCOUNT_E2E_PHASE}`;
  const identifier = `mm75-${run}`;
  const password = createHash('sha256').update(run).digest('hex');
  await page.goto('/register');
  await page.getByLabel('Логин', { exact: true }).fill(identifier);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Создать аккаунт', exact: true }).click();
  await expect(
    page.getByText('Запрос обработан. Теперь войдите с вашим логином и паролем.'),
  ).toBeVisible();
  await expect
    .poll(() =>
      bodies.some((body) => JSON.parse(body).event_name === 'registration_request_completed'),
    )
    .toBe(true);
  expect(privateHeader).toBe(false);
  expect(
    bodies.some(
      (body) => body.includes(identifier) || body.includes(password) || body.includes(queryCanary),
    ),
  ).toBe(false);
  await page.route('**/analytics/track', (route) => route.abort());
  await page.getByLabel('Логин', { exact: true }).fill(identifier);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page).toHaveURL(/\/account$/);
});
