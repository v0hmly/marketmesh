import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import { ProfileSchema, UserService } from '@marketmesh/api/user/v1/user_pb';
import { SellerService, ShopSchema, ShopStatus } from '@marketmesh/api/seller/v1/seller_pb';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import AxeBuilder from '@axe-core/playwright';
import { expect, test, type BrowserContext, type Route } from '@playwright/test';

// Contract-shaped UI fixture, deliberately separate from the full backend E2E.
async function browserApi(context: BrowserContext) {
  let signedIn = false;
  let applications = 0;
  const profile = create(ProfileSchema, {
    subjectId: new Uint8Array(16).fill(1),
    displayName: 'Анна',
    version: 1n,
  });
  const shop = create(ShopSchema, {
    shopId: new Uint8Array(16).fill(7),
    shopName: 'Мастерская «Глина и соль»',
    status: ShopStatus.APPROVED,
    createdAtUnix: 1n,
  });
  async function jsonError(route: Route, code: string, status: number) {
    await route.fulfill({
      status,
      headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
      body: JSON.stringify({ code, message: 'Request failed' }),
    });
  }
  await context.route('**/auth.v1.AuthService/**', async (route) => {
    const method = new URL(route.request().url()).pathname.split('/').at(-1);
    if (method === 'StartLogin') {
      const input = fromBinary(
        AuthService.method.startLogin.input,
        route.request().postDataBuffer()!,
      );
      expect(input.identifier).toBe('anna@example.ru');
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.startLogin.output,
            create(AuthService.method.startLogin.output, {
              loginChallengeId: new Uint8Array(16).fill(9),
              codeExpiresInSeconds: 600n,
            }),
          ),
        ),
      });
    } else if (method === 'CompleteLogin') {
      const input = fromBinary(
        AuthService.method.completeLogin.input,
        route.request().postDataBuffer()!,
      );
      if (input.code !== '482913') {
        const detail = toBinary(
          ErrorInfoSchema,
          create(ErrorInfoSchema, { domain: 'marketmesh.auth', reason: 'CODE_MISMATCH' }),
        );
        await route.fulfill({
          status: 400,
          headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
          body: JSON.stringify({
            code: 'invalid_argument',
            message: 'wrong code',
            details: [
              { type: 'google.rpc.ErrorInfo', value: Buffer.from(detail).toString('base64') },
            ],
          }),
        });
        return;
      }
      signedIn = true;
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.completeLogin.output,
            create(AuthService.method.completeLogin.output, { subjectId: profile.subjectId }),
          ),
        ),
      });
    } else await jsonError(route, 'unimplemented', 501);
  });
  await context.route('**/user.v1.UserService/**', async (route) => {
    if (!signedIn) {
      await jsonError(route, 'unauthenticated', 401);
      return;
    }
    await route.fulfill({
      headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
      body: Buffer.from(
        toBinary(
          UserService.method.getMe.output,
          create(UserService.method.getMe.output, { profile }),
        ),
      ),
    });
  });
  await context.route('**/seller.v1.SellerService/**', async (route) => {
    const method = new URL(route.request().url()).pathname.split('/').at(-1);
    if (method === 'SubmitApplication') {
      const input = fromBinary(
        SellerService.method.submitApplication.input,
        route.request().postDataBuffer()!,
      );
      expect(input.email).toBe('anna@example.ru');
      expect(input.inn).toBe('7707083893');
      expect(input.shopName).toBe('Мастерская «Глина и соль»');
      applications++;
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.alloc(0),
      });
      return;
    }
    if (!signedIn) {
      await jsonError(route, 'unauthenticated', 401);
      return;
    }
    await route.fulfill({
      headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
      body: Buffer.from(
        toBinary(
          SellerService.method.getMyShop.output,
          create(SellerService.method.getMyShop.output, { shop }),
        ),
      ),
    });
  });
  return {
    applications: () => applications,
  };
}

test('seller login requires the emailed code and opens the dashboard accessibly', async ({
  page,
  context,
}) => {
  await browserApi(context);
  await page.goto('/seller/login');
  await page.getByLabel('Рабочая почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByRole('button', { name: 'Войти в портал' }).click();
  await expect(page.getByRole('heading', { name: 'Подтвердите вход.' })).toBeVisible();
  const code = page.getByLabel('Код из письма', { exact: true });
  await code.fill('000000');
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(
    page.getByText('Код неверный. Проверьте письмо и введите код ещё раз.'),
  ).toBeVisible();
  await code.fill('482913');
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Обзор', exact: true })).toBeVisible();
  await expect(page.getByText('Магазин «Мастерская «Глина и соль»».')).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByRole('link', { name: 'Изделия', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Изделия', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'Заказы', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Заказ № 1501-0102' })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test('seller application validates the INN, submits and stays accessible', async ({
  page,
  context,
}) => {
  const api = await browserApi(context);
  await page.goto('/seller/apply');
  await page.getByLabel('Рабочая почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('Secret123!');
  await page.getByLabel('Повторите пароль', { exact: true }).fill('Secret123!');
  await page.getByLabel('Название магазина', { exact: true }).fill('Мастерская «Глина и соль»');
  await page.getByLabel('ИНН', { exact: true }).fill('770708389');
  await page.getByRole('button', { name: 'Отправить заявку' }).click();
  await expect(page.getByText('ИНН состоит из 10 или 12 цифр.')).toBeVisible();
  expect(api.applications()).toBe(0);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByLabel('ИНН', { exact: true }).fill('7707083893');
  await page.getByRole('button', { name: 'Отправить заявку' }).click();
  await expect(page.getByText('Заявка отправлена.')).toBeVisible();
  expect(api.applications()).toBe(1);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByRole('link', { name: 'Перейти к входу' }).click();
  await expect(page).toHaveURL(/\/seller\/login$/);
});
