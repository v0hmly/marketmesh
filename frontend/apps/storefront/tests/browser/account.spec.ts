import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import { ProfileSchema, UserService } from '@marketmesh/api/user/v1/user_pb';
import AxeBuilder from '@axe-core/playwright';
import { expect, test, type BrowserContext, type Route } from '@playwright/test';

// Contract-shaped UI fixture, deliberately separate from the full backend E2E.
async function browserApi(context: BrowserContext) {
  let signedIn = false;
  let accessLive = false;
  let updates = 0;
  let refreshes = 0;
  let refreshGate: Promise<void> | undefined;
  let profile = create(ProfileSchema, {
    subjectId: new Uint8Array(16).fill(1),
    displayName: 'Анна',
    bio: 'Люблю керамику',
    version: 1n,
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
    if (method === 'RegisterCredentials') {
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.alloc(0),
      });
    } else if (method === 'Login') {
      const input = fromBinary(AuthService.method.login.input, route.request().postDataBuffer()!);
      expect(input.identifier).toBe('anna');
      signedIn = true;
      accessLive = true;
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.login.output,
            create(AuthService.method.login.output, { subjectId: profile.subjectId }),
          ),
        ),
      });
    } else if (method === 'RefreshSession' && signedIn) {
      refreshes++;
      await refreshGate;
      accessLive = true;
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.alloc(0),
      });
    } else if (method === 'Logout' || method === 'LogoutAll') {
      signedIn = false;
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.alloc(0),
      });
    } else await jsonError(route, 'unauthenticated', 401);
  });
  await context.route('**/user.v1.UserService/**', async (route) => {
    if (!signedIn || !accessLive) {
      await jsonError(route, 'unauthenticated', 401);
      return;
    }
    const method = new URL(route.request().url()).pathname.split('/').at(-1);
    if (method === 'UpdateMe') {
      updates++;
      const input = fromBinary(
        UserService.method.updateMe.input,
        route.request().postDataBuffer()!,
      );
      if (input.expectedVersion !== profile.version) {
        await jsonError(route, 'aborted', 409);
        return;
      }
      profile = create(ProfileSchema, {
        ...profile,
        displayName: input.displayName.trim(),
        bio: input.bio,
        version: profile.version + 1n,
      });
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
  return {
    updates: () => updates,
    refreshes: () => refreshes,
    expireAndHoldRefresh() {
      accessLive = false;
      let release!: () => void;
      refreshGate = new Promise<void>((resolve) => {
        release = resolve;
      });
      return release;
    },
  };
}

test('registration, profile editing, reload, keyboard labels and accessible layouts', async ({
  page,
  context,
}, testInfo) => {
  const api = await browserApi(context);
  await page.goto('/register');
  await page.getByLabel('Логин', { exact: true }).fill('anna');
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  await page.getByRole('button', { name: 'Создать аккаунт' }).click();
  await expect(
    page.getByText('Запрос обработан. Теперь войдите с вашим логином и паролем.'),
  ).toBeVisible();
  await expect(page.getByLabel('Пароль', { exact: true })).toHaveValue('');
  const loginA11y = await new AxeBuilder({ page }).analyze();
  expect(loginA11y.violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('login-desktop.png'), fullPage: true });
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByLabel('Имя', { exact: true })).toHaveValue('Анна');
  await page
    .getByRole('textbox', { name: 'О себе', exact: true })
    .fill('<script>не HTML</script>\nЛюблю вещи ручной работы.');
  await page.getByRole('button', { name: 'Сохранить изменения' }).click();
  await expect(page.getByText('Изменения сохранены.', { exact: true })).toBeVisible();
  expect(api.updates()).toBe(1);
  await page.reload();
  await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveValue(
    '<script>не HTML</script>\nЛюблю вещи ручной работы.',
  );
  expect(await page.locator('script:not([src])').count()).toBe(0);
  const storage = await page.evaluate(() => JSON.stringify({ ...localStorage }));
  expect(storage).not.toContain('Анна');
  expect(storage).not.toContain('anna');
  expect(storage).not.toContain('password');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('profile-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('profile-mobile.png'), fullPage: true });
});

test('logout in another tab clears an unsaved private draft', async ({ page, context }) => {
  await browserApi(context);
  await page.goto('/login');
  await page.getByLabel('Логин', { exact: true }).fill('anna');
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByLabel('Имя', { exact: true })).toHaveValue('Анна');
  await page
    .getByRole('textbox', { name: 'О себе', exact: true })
    .fill('Несохранённый личный черновик');
  const second = await context.newPage();
  await second.goto('/account');
  await expect(second.getByLabel('Имя', { exact: true })).toHaveValue('Анна');
  await second.getByRole('button', { name: 'Выйти', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveCount(0);
  await expect(page.getByText('Перейти ко входу')).toBeVisible();
  expect(await page.locator('body').textContent()).not.toContain('Несохранённый личный черновик');
});

test('an active refresh is shared by new tabs and preserves an existing draft', async ({
  page,
  context,
}) => {
  const api = await browserApi(context);
  await page.goto('/login');
  await page.getByLabel('Логин', { exact: true }).fill('anna');
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByLabel('Имя', { exact: true })).toHaveValue('Анна');
  await page
    .getByRole('textbox', { name: 'О себе', exact: true })
    .fill('Черновик переживёт обновление сессии');
  const release = api.expireAndHoldRefresh();
  try {
    const second = await context.newPage();
    await second.goto('/account');
    await expect.poll(api.refreshes).toBe(1);
    const third = await context.newPage();
    await third.goto('/account');
    await expect(
      third.getByText('Результат операции с сессией неизвестен.', { exact: false }),
    ).toHaveCount(0);
    release();
    await expect(second.getByLabel('Имя', { exact: true })).toHaveValue('Анна');
    await expect(third.getByLabel('Имя', { exact: true })).toHaveValue('Анна');
    await expect(page.getByRole('textbox', { name: 'О себе', exact: true })).toHaveValue(
      'Черновик переживёт обновление сессии',
    );
    expect(api.refreshes()).toBe(1);
  } finally {
    release();
  }
});
