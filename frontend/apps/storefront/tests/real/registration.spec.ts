import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { letter, letterLink, submitLogin } from './mail';

const password = 'CorrectHorse9!';
async function register(page: Page, email: string) {
  await page.goto('/register');
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByLabel('Повторите пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Зарегистрироваться', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Аккаунт создан.' })).toBeVisible();
  return letterLink(await letter(email, 'Подтвердите почту в MarketMesh'), 'verify');
}

test('registration link logs in its browser and synchronizes the original tab; later login needs a code', async ({
  page,
  context,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  const email = `registration-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`;
  const link = await register(page, email);
  const cookies = await context.cookies();
  const binding = cookies.find((cookie) => cookie.name === '__Host-mm-registration');
  expect(Boolean(binding?.secure && binding.httpOnly && binding.sameSite === 'Strict')).toBe(true);
  expect(
    cookies.some((cookie) => ['__Host-mm-access', '__Host-mm-refresh'].includes(cookie.name)),
  ).toBe(false);
  expect(await page.evaluate(() => document.cookie.includes('__Host-mm-registration'))).toBe(false);
  const confirmation = await context.newPage();
  await confirmation.goto(link);
  await expect.poll(() => new URL(confirmation.url()).hash).toBe('');
  expect((await context.cookies()).some((cookie) => cookie.name === '__Host-mm-access')).toBe(
    false,
  );
  expect(
    (await new AxeBuilder({ page: confirmation }).analyze()).violations.map((item) => item.id),
  ).toEqual([]);
  await confirmation.getByRole('button', { name: 'Подтвердить почту', exact: true }).click();
  await expect(confirmation).toHaveURL(/\/account\/id$/);
  await expect(page).toHaveURL(/\/account\/id$/);
  await expect(
    confirmation.getByRole('button', { name: 'Изменить данные', exact: true }),
  ).toBeVisible();
  expect((await context.cookies()).some((cookie) => cookie.name === '__Host-mm-registration')).toBe(
    false,
  );
  await confirmation.getByRole('button', { name: 'Выйти из аккаунта', exact: true }).click();
  await expect(confirmation).toHaveURL(/\/login$/);
  await confirmation.goto(link);
  await confirmation.getByRole('button', { name: 'Подтвердить почту', exact: true }).click();
  await expect(confirmation.getByRole('alert')).toBeVisible();
  expect((await context.cookies()).some((cookie) => cookie.name === '__Host-mm-access')).toBe(
    false,
  );
  await confirmation.goto('/login');
  await confirmation.getByLabel('Почта', { exact: true }).fill(email);
  await confirmation.getByLabel('Пароль', { exact: true }).fill(password);
  await submitLogin(confirmation, email);
  await expect(confirmation).toHaveURL(/\/account\/id$/);
});

test('a forwarded registration link confirms email without creating a session in another browser', async ({
  page,
  browser,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  const email = `registration-foreign-${process.env.ACCOUNT_E2E_RUN_ID}@example.test`;
  const link = await register(page, email);
  const other = await browser.newContext();
  try {
    const foreign = await other.newPage();
    await foreign.goto(new URL(link, page.url()).href);
    await foreign.getByRole('button', { name: 'Подтвердить почту', exact: true }).click();
    await expect(foreign.getByRole('status')).toContainText('Почта подтверждена');
    expect((await other.cookies()).some((cookie) => cookie.name.startsWith('__Host-mm-'))).toBe(
      false,
    );
    // The registration browser does not acquire a session just because somebody else clicked.
    expect(
      (await page.context().cookies()).some((cookie) => cookie.name === '__Host-mm-access'),
    ).toBe(false);
  } finally {
    await other.close();
  }
});
