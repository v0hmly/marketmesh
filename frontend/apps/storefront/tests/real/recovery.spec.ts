import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { letter, letterCode, verifyEmail } from './mail';

const password = 'RecoveryHorse9!';
async function signIn(page: Page, email: string) {
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
}
async function setup(page: Page, email: string) {
  await page.goto('/register');
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByLabel('Повторите пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Зарегистрироваться', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Аккаунт создан.' })).toBeVisible();
  await verifyEmail(page, email);
  await signIn(page, email);
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  await page.getByLabel('Пароль для настройки входа').fill(password);
  await page.getByRole('button', { name: 'Включить подтверждение входа', exact: true }).click();
  const enable = await letter(email, 'Код для входа в MarketMesh');
  await page.getByLabel('Код из письма', { exact: true }).fill(letterCode(enable));
  await page.getByRole('button', { name: 'Подтвердить настройку', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Настройка входа изменена');
  await signIn(page, email);
  const login = await letter(email, 'Код для входа в MarketMesh', new Set([enable.ID]));
  await page.getByLabel('Код из письма', { exact: true }).fill(letterCode(login));
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  return new Set([enable.ID, login.ID]);
}
async function start(page: Page, email: string, seen: Set<string>) {
  await page.getByLabel('Пароль для резервных кодов').fill(password);
  await page.getByRole('button', { name: 'Запросить резервные коды', exact: true }).click();
  const message = await letter(email, 'Подтвердите замену резервных кодов', seen);
  seen.add(message.ID);
  await page.getByLabel('Код для резервного набора').fill(letterCode(message));
}
async function logout(page: Page) {
  await page.getByRole('button', { name: 'Выйти', exact: true }).click();
  await expect(page).toHaveURL(/\/login$/);
}
async function recovery(page: Page, value: string) {
  await page.getByLabel('Резервный код', { exact: true }).fill(value);
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
}
test('single-display recovery set replaces old codes and authenticates once through real Auth and Mailpit', async ({
  page,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  test.setTimeout(180_000);
  const email = `recovery-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`;
  const seen = await setup(page, email);
  const started = Date.now();
  await start(page, email, seen);
  const generated = page.waitForResponse(
    (response) => new URL(response.url()).pathname === '/auth.v1.AuthService/CompleteRecoveryCodes',
  );
  await page.getByRole('button', { name: 'Создать новый набор', exact: true }).click();
  expect((await generated).headers()['cache-control']).toBe('no-store');
  const list = page.getByRole('list', { name: 'Новый набор резервных кодов' });
  await expect(list.getByRole('listitem')).toHaveCount(8);
  const codes = await list.locator('code').allTextContents();
  expect(codes.every((value) => /^[a-f0-9]{8}(?:-[a-f0-9]{8}){3}$/.test(value))).toBe(true);
  expect(new Set(codes).size).toBe(8);
  expect((await new AxeBuilder({ page }).analyze()).violations.map((item) => item.id)).toEqual([]);
  const notice = await letter(email, 'Резервные коды обновлены');
  expect(codes.some((code) => JSON.stringify(notice).includes(code))).toBe(false);
  expect(
    await page.evaluate(
      (values) =>
        values.some((value) =>
          JSON.stringify({ ...localStorage, ...sessionStorage }).includes(value),
        ),
      codes,
    ),
  ).toBe(false);
  await logout(page);
  await signIn(page, email);
  await page.getByRole('button', { name: 'Использовать резервный код', exact: true }).click();
  expect((await new AxeBuilder({ page }).analyze()).violations.map((item) => item.id)).toEqual([]);
  await recovery(page, codes[0]!);
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  await expect(page.getByText('Осталось кодов: 7 из 8.')).toBeVisible();
  await expect(page.getByRole('list', { name: 'Новый набор резервных кодов' })).toHaveCount(0);
  await logout(page);
  await signIn(page, email);
  await page.getByRole('button', { name: 'Использовать резервный код', exact: true }).click();
  await recovery(page, codes[0]!);
  await expect(page.getByRole('alert')).toContainText('Резервный код не принят');
  await recovery(page, codes[1]!);
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  // Respect the real generation cooldown; no clock/DB shortcut in browser E2E.
  await page.waitForTimeout(Math.max(0, 62_000 - (Date.now() - started)));
  await start(page, email, seen);
  await page.getByRole('button', { name: 'Создать новый набор', exact: true }).click();
  await expect(list.getByRole('listitem')).toHaveCount(8);
  const next = await list.locator('code').allTextContents();
  await logout(page);
  await signIn(page, email);
  await page.getByRole('button', { name: 'Использовать резервный код', exact: true }).click();
  await recovery(page, codes[2]!);
  await expect(page.getByRole('alert')).toContainText('Резервный код не принят');
  await recovery(page, next[0]!);
  await expect(page).toHaveURL(/\/account$/);
  codes.fill('');
  next.fill('');
});

test('lost recovery generation response is not replayed or recovered from a later read', async ({
  page,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  const email = `recovery-lost-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`;
  const seen = await setup(page, email);
  await start(page, email, seen);
  let writes = 0;
  await page.route('**/auth.v1.AuthService/CompleteRecoveryCodes', async (route) => {
    writes++;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    await route.abort('failed');
  });
  await page.getByRole('button', { name: 'Создать новый набор', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('показать его повторно нельзя');
  expect(writes).toBe(1);
  await page.unroute('**/auth.v1.AuthService/CompleteRecoveryCodes');
  await page.reload();
  await expect(page.getByText('Осталось кодов: 8 из 8.')).toBeVisible();
  await expect(page.getByRole('list', { name: 'Новый набор резервных кодов' })).toHaveCount(0);
});
