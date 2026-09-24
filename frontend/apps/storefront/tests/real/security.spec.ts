import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { letter, letterCode, letterLink, verifyEmail, submitLogin } from './mail';

test.use({ timezoneId: 'Europe/Moscow' });

const run = process.env.ACCOUNT_E2E_RUN_ID!;
const email = `security-${run}@example.test`;
const password = 'CorrectHorse9!';
async function signIn(page: Page, address: string, value: string, complete = true) {
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill(address);
  await page.getByLabel('Пароль', { exact: true }).fill(value);
  if (complete) await submitLogin(page, address);
  else await page.getByRole('button', { name: 'Войти', exact: true }).click();
}
async function accessible(page: Page) {
  const result = await new AxeBuilder({ page }).analyze();
  expect(result.violations.map((item) => item.id)).toEqual([]);
}
test('email verification, default code, reissued code, reset and security notifications through Mailpit', async ({
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
  const verification = await letter(email, 'Подтвердите почту в MarketMesh');
  expect(verification.Text.includes('24 часа с момента запроса')).toBe(true);
  expect(/Подтвердите почту до .* \(UTC\+3\)\./.test(verification.Text)).toBe(true);
  await signIn(page, email, password, false);
  await expect(page.getByRole('alert')).toContainText('Подтвердите почту');
  await verifyEmail(page, email);
  await expect(page).toHaveURL(/\/account\/id$/);
  await page.goto('/account/security');
  await expect(page).toHaveURL(/\/account\/id#security$/);
  await expect(page.locator('.security-row').filter({ hasText: 'Почта для входа' })).toContainText(
    email,
  );
  await accessible(page);
  await expect(
    page.getByRole('button', { name: 'Выйти на всех устройствах', exact: true }),
  ).toBeDisabled();
  await expect(page.locator('.security-row').filter({ hasText: 'Код при входе' })).toContainText(
    'После пароля требуется код из письма',
  );
  await page.getByRole('button', { name: 'Выйти из аккаунта', exact: true }).click();
  await expect(page).toHaveURL(/\/login$/);
  await signIn(page, email, password, false);
  const loginMail = await letter(email, 'Код для входа в MarketMesh');
  expect(/(?:10 минут|9 минут(?: \d+ секунд[уы]?)?) с момента запроса/.test(loginMail.Text)).toBe(
    true,
  );
  expect(/Используйте код до .* \(UTC\+3\)\./.test(loginMail.Text)).toBe(true);
  const wrong = letterCode(loginMail) === '000000' ? '000001' : '000000';
  for (let i = 0; i < 3; i++) {
    await page.getByLabel('Код из письма', { exact: true }).fill(wrong);
    await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
    await expect(page.getByRole('alert')).toBeVisible();
  }
  const reissued = await letter(email, 'Код для входа в MarketMesh', new Set([loginMail.ID]));
  const deadline = (text: string) => text.match(/Используйте код до ([^\n]+)\./)?.[1];
  expect(deadline(reissued.Text) === deadline(loginMail.Text)).toBe(true);
  expect(reissued.Text.includes('с момента запроса')).toBe(true);
  await page.getByLabel('Код из письма', { exact: true }).fill(letterCode(reissued));
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(page).toHaveURL(/\/account\/id$/);
  await letter(email, 'Вход в аккаунт MarketMesh');
  await page.goto('/account/security/reset');
  await accessible(page);
  await page.getByLabel('Почта', { exact: true }).fill(email);
  await page.getByRole('button', { name: 'Отправить письмо', exact: true }).click();
  const reset = await letter(email, 'Сброс пароля в MarketMesh');
  expect(reset.Text.includes('30 минут с момента запроса')).toBe(true);
  expect(/Смените пароль до .* \(UTC\+3\)\./.test(reset.Text)).toBe(true);
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
  await expect(page.getByRole('button', { name: 'Получить новую ссылку' })).toBeVisible();
  await accessible(page);
  await page.getByRole('button', { name: 'Получить новую ссылку' }).click();
  await expect(page.getByLabel('Почта', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Новый пароль', { exact: true })).toHaveCount(0);
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
  await expect(page).toHaveURL(/\/account\/id$/);
  await page.goto('/account/security');
  const loginEmail = page.locator('.security-row').filter({ hasText: 'Почта для входа' });
  await expect(loginEmail).toContainText(oldAddress);
  await page.getByRole('button', { name: 'Сменить почту', exact: true }).click();
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
  await signIn(page, oldAddress, password, false);
  await expect(page.getByRole('alert')).toContainText('Почта или пароль указаны неверно');
  await signIn(page, newAddress, password);
  await expect(page).toHaveURL(/\/account\/id$/);
  await page.goto('/account/security');
  await expect(loginEmail).toContainText(newAddress);
});
