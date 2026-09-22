import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { letter, letterCode, letterLink, verifyEmail } from './mail';

const run = process.env.ACCOUNT_E2E_RUN_ID!;
const email = `security-${run}@example.test`;
const password = 'CorrectHorse9!';
async function signIn(page: Page, address: string, value: string) {
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill(address);
  await page.getByLabel('Пароль', { exact: true }).fill(value);
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
}
async function accessible(page: Page) {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations.map((item) => item.id)).toEqual([]);
}
test('email verification, optional code, reissued code, reset and security notifications through Mailpit', async ({
  page,
  context,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  await page.goto('/register');
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByLabel('Повторите пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Зарегистрироваться', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Аккаунт создан.' })).toBeVisible();
  await signIn(page, email, password);
  await expect(page.getByRole('alert')).toContainText('Подтвердите почту');
  await verifyEmail(page, email);
  await signIn(page, email, password);
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  await expect(page.getByText(email, { exact: true })).toBeVisible();
  await accessible(page);
  await expect(
    page.getByRole('button', { name: 'Закрыть все сеансы', exact: true }),
  ).toBeDisabled();
  const oldCookies = await context.cookies();
  await page.getByLabel('Пароль для настройки входа').fill(password);
  await page.getByRole('button', { name: 'Включить подтверждение входа', exact: true }).click();
  const enableMail = await letter(email, 'Код для входа в MarketMesh');
  const enableCode = letterCode(enableMail);
  await page
    .getByLabel('Код из письма', { exact: true })
    .fill(enableCode === '000000' ? '000001' : '000000');
  await page.getByRole('button', { name: 'Подтвердить настройку', exact: true }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await expect(page.getByLabel('Код из письма', { exact: true })).toBeVisible();
  await page.getByLabel('Код из письма', { exact: true }).fill(enableCode);
  await page.getByRole('button', { name: 'Подтвердить настройку', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Настройка входа изменена');
  await letter(email, 'Подтверждение входа включено');
  expect((await context.cookies()).filter((c) => c.name.startsWith('__Host-mm-')).length).toBe(0);
  await context.addCookies(oldCookies);
  await page.goto('/account');
  await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveCount(0);
  await context.clearCookies();
  await signIn(page, email, password);
  const loginMail = await letter(email, 'Код для входа в MarketMesh', new Set([enableMail.ID]));
  const wrong = letterCode(loginMail) === '000000' ? '000001' : '000000';
  for (let i = 0; i < 3; i++) {
    await page.getByLabel('Код из письма', { exact: true }).fill(wrong);
    await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
    await expect(page.getByRole('alert')).toBeVisible();
  }
  const reissued = await letter(
    email,
    'Код для входа в MarketMesh',
    new Set([enableMail.ID, loginMail.ID]),
  );
  await page.getByLabel('Код из письма', { exact: true }).fill(letterCode(reissued));
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(page).toHaveURL(/\/account$/);
  await letter(email, 'Вход в аккаунт MarketMesh');
  await page.goto('/account/security/reset');
  await accessible(page);
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByRole('button', { name: 'Отправить письмо', exact: true }).click();
  const reset = await letter(email, 'Сброс пароля в MarketMesh');
  await page.goto(letterLink(reset, 'reset'));
  await expect.poll(() => new URL(page.url()).hash).toBe('');
  const next = 'NewCorrectHorse8!';
  await page.getByLabel('Новый пароль', { exact: true }).fill(next);
  await page.getByLabel('Повторите новый пароль', { exact: true }).fill(next);
  await accessible(page);
  await page.getByRole('button', { name: 'Сохранить новый пароль', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Пароль изменён');
  await letter(email, 'Пароль от аккаунта изменён');
  expect((await context.cookies()).filter((c) => c.name.startsWith('__Host-mm-')).length).toBe(0);
  const link = letterLink(reset, 'reset');
  const token = new URL(link, 'https://local.test').hash.slice(1);
  expect(
    await page.evaluate(
      (secret) =>
        JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }).includes(
          secret,
        ),
      token,
    ),
  ).toBe(false);
  await page.goto(link);
  await page.getByLabel('Новый пароль', { exact: true }).fill('AnotherPassword3!');
  await page.getByLabel('Повторите новый пароль', { exact: true }).fill('AnotherPassword3!');
  await page.getByRole('button', { name: 'Сохранить новый пароль', exact: true }).click();
  await expect(page.getByRole('alert')).toBeVisible();
});

test('email change requires password and confirmation at the new address', async ({
  page,
  context,
}) => {
  test.skip(process.env.ACCOUNT_E2E_PHASE !== 'core');
  const oldAddress = `old-${run}@example.test`;
  const newAddress = `new-${run}@example.test`;
  await page.goto('/register');
  await page.getByLabel('Почта', { exact: true }).fill(oldAddress);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  await page.getByLabel('Повторите пароль', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Зарегистрироваться', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Аккаунт создан.' })).toBeVisible();
  await verifyEmail(page, oldAddress);
  await signIn(page, oldAddress, password);
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  await expect(page.getByText(oldAddress, { exact: true })).toBeVisible();
  await page.getByLabel('Новая почта', { exact: true }).fill(newAddress);
  await page.getByLabel('Текущий пароль для смены почты').fill('WrongPassword1!');
  await page.getByRole('button', { name: 'Отправить подтверждение', exact: true }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await page.getByLabel('Текущий пароль для смены почты').fill(password);
  await page.getByRole('button', { name: 'Отправить подтверждение', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Письмо подтверждения отправлено');
  await letter(oldAddress, 'Запрошена смена адреса почты');
  const confirm = await letter(newAddress, 'Подтвердите новый адрес почты');
  await page.goto(letterLink(confirm, 'change_email'));
  await page.getByRole('button', { name: 'Подтвердить действие', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('Почта изменена');
  expect((await context.cookies()).filter((c) => c.name.startsWith('__Host-mm-')).length).toBe(0);
  await signIn(page, oldAddress, password);
  await expect(page.getByRole('alert')).toContainText('Почта или пароль указаны неверно');
  await signIn(page, newAddress, password);
  await expect(page).toHaveURL(/\/account$/);
  await page.goto('/account/security');
  await expect(page.getByText(newAddress, { exact: true })).toBeVisible();
});
