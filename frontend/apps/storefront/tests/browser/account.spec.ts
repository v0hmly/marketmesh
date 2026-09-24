import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import {
  AccountSettingsSchema,
  Theme,
  AddressBookSchema,
  AddressSchema,
  ProfileSchema,
  UserService,
} from '@marketmesh/api/user/v1/user_pb';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import AxeBuilder from '@axe-core/playwright';
import { expect, test, type BrowserContext, type Route } from '@playwright/test';

// Contract-shaped UI fixture, deliberately separate from the full backend E2E.
async function browserApi(context: BrowserContext) {
  let signedIn = false;
  let accessLive = false;
  let codeFlow = false;
  let updates = 0;
  let refreshes = 0;
  let settingsWrites = 0;
  let loseSettingsReply = false;
  let settings = create(AccountSettingsSchema, {
    subjectId: new Uint8Array(16).fill(1),
    version: 1n,
    theme: Theme.SYSTEM,
  });
  let addressWrites = 0;
  let loseCreateReply = false;
  let nextAddress = 0;
  let book = create(AddressBookSchema, { subjectId: new Uint8Array(16).fill(1), version: 1n });
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
    } else if (method === 'StartLogin') {
      if (!codeFlow) {
        await jsonError(route, 'unimplemented', 501);
        return;
      }
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
    } else if (method === 'ResendLoginCode') {
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.resendLoginCode.output,
            create(AuthService.method.resendLoginCode.output, { codeExpiresInSeconds: 600n }),
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
      accessLive = true;
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.completeLogin.output,
            create(AuthService.method.completeLogin.output, { subjectId: profile.subjectId }),
          ),
        ),
      });
    } else if (method === 'RequestEmailVerification') {
      if (!codeFlow) {
        await jsonError(route, 'unimplemented', 501);
        return;
      }
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.alloc(0),
      });
    } else if (method === 'Login') {
      const input = fromBinary(AuthService.method.login.input, route.request().postDataBuffer()!);
      expect(input.identifier).toBe('anna@example.ru');
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
    } else if (method === 'GetCredentials' && signedIn && accessLive) {
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.getCredentials.output,
            create(AuthService.method.getCredentials.output, {
              email: 'anna@example.ru',
              emailVerified: true,
            }),
          ),
        ),
      });
    } else if (method === 'ListSessions' && signedIn && accessLive) {
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            AuthService.method.listSessions.output,
            create(AuthService.method.listSessions.output, {
              sessions: [
                {
                  sessionId: new Uint8Array(16).fill(5),
                  device: 'MacBook Air, macOS',
                  browser: 'Safari',
                  location: 'Санкт-Петербург, Россия',
                  createdAtUnix: 1790000000n,
                  lastSeenAtUnix: 1790003600n,
                  current: true,
                },
              ],
            }),
          ),
        ),
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
    if (method === 'GetSettings' || method === 'UpdateSettings') {
      if (method === 'UpdateSettings') {
        settingsWrites++;
        const input = fromBinary(
          UserService.method.updateSettings.input,
          route.request().postDataBuffer()!,
        );
        if (input.expectedVersion !== settings.version) {
          await jsonError(route, 'aborted', 409);
          return;
        }
        settings = create(AccountSettingsSchema, {
          ...settings,
          theme: input.theme,
          version: settings.version + 1n,
        });
        if (loseSettingsReply) {
          loseSettingsReply = false;
          await route.abort('failed');
          return;
        }
      }
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            UserService.method.getSettings.output,
            create(UserService.method.getSettings.output, { settings }),
          ),
        ),
      });
      return;
    }
    const addressMethods = {
      ListAddresses: UserService.method.listAddresses,
      CreateAddress: UserService.method.createAddress,
      UpdateAddress: UserService.method.updateAddress,
      DeleteAddress: UserService.method.deleteAddress,
      SetDefaultAddress: UserService.method.setDefaultAddress,
    };
    if (method && method in addressMethods) {
      if (method !== 'ListAddresses') {
        addressWrites++;
        // The address request fields intentionally share their declared wire types.
        const input =
          method === 'CreateAddress'
            ? fromBinary(UserService.method.createAddress.input, route.request().postDataBuffer()!)
            : method === 'UpdateAddress'
              ? fromBinary(
                  UserService.method.updateAddress.input,
                  route.request().postDataBuffer()!,
                )
              : fromBinary(
                  UserService.method.deleteAddress.input,
                  route.request().postDataBuffer()!,
                );
        if (input.expectedBookVersion !== book.version) {
          await jsonError(route, 'aborted', 409);
          return;
        }
        const id = 'addressId' in input ? Array.from(input.addressId).join(',') : '';
        if (method === 'CreateAddress' && 'fields' in input)
          book.addresses.push(
            create(AddressSchema, {
              addressId: new Uint8Array(16).fill(++nextAddress),
              fields: input.fields,
              isDefault: book.addresses.length === 0,
            }),
          );
        else if (method === 'DeleteAddress')
          book.addresses = book.addresses.filter(
            (address) => Array.from(address.addressId).join(',') !== id,
          );
        else
          for (const address of book.addresses) {
            const selected = Array.from(address.addressId).join(',') === id;
            if (method === 'SetDefaultAddress') address.isDefault = selected;
            else if (selected && 'fields' in input) address.fields = input.fields;
          }
        book = create(AddressBookSchema, { ...book, version: book.version + 1n });
        if (method === 'CreateAddress' && loseCreateReply) {
          loseCreateReply = false;
          await route.abort('failed');
          return;
        }
      }
      await route.fulfill({
        headers: { 'content-type': 'application/proto', 'cache-control': 'no-store' },
        body: Buffer.from(
          toBinary(
            UserService.method.listAddresses.output,
            create(UserService.method.listAddresses.output, { book }),
          ),
        ),
      });
      return;
    }
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
        lastName: input.lastName,
        birthDate: input.birthDate,
        gender: input.gender,
        phone: input.phone,
        city: input.city,
        showAge: input.showAge,
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
    enableCodeFlow() {
      codeFlow = true;
    },
    settingsWrites: () => settingsWrites,
    loseNextSettingsReply() {
      loseSettingsReply = true;
    },
    loseNextCreateReply() {
      loseCreateReply = true;
    },
    addressWrites: () => addressWrites,
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

async function buyerLogin(page: import('@playwright/test').Page) {
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Заказы', exact: true })).toBeVisible();
}
async function openIdDraft(page: import('@playwright/test').Page, city: string) {
  await page.getByRole('link', { name: 'MarketMesh ID', exact: true }).click();
  await page.getByRole('button', { name: 'Изменить данные' }).click();
  await expect(page.getByRole('textbox', { name: 'Имя', exact: true })).toHaveValue('Анна');
  await page.getByLabel('Город проживания', { exact: true }).fill(city);
}

test('registration, cabinet layout, MarketMesh ID editing, reload and accessible layouts', async ({
  page,
  context,
}, testInfo) => {
  const api = await browserApi(context);
  await page.goto('/register');
  await page.getByLabel('Почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('Secret123!');
  await page.getByLabel('Повторите пароль', { exact: true }).fill('Secret123!');
  await page.getByRole('button', { name: 'Зарегистрироваться' }).click();
  await expect(page.getByText('Аккаунт создан.')).toBeVisible();
  await expect(
    page.getByText('Запрос обработан. Теперь войдите с вашей почтой и паролем.'),
  ).toBeVisible();
  const loginA11y = await new AxeBuilder({ page }).analyze();
  expect(loginA11y.violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('login-desktop.png'), fullPage: true });
  await page.getByRole('link', { name: 'Войти' }).click();
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('Почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('Secret123!');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page).toHaveURL(/\/account\/orders$/);
  const sections = page.getByRole('navigation', { name: 'Разделы личного кабинета' });
  await expect(sections.getByRole('link')).toHaveText([
    /^Заказы/,
    /^Избранное/,
    /^Отзывы/,
    'Адреса доставки',
    'MarketMesh ID',
  ]);
  await expect(sections.getByRole('link', { name: /^Заказы/ })).toHaveAttribute(
    'aria-current',
    'page',
  );
  await expect(page.getByRole('heading', { level: 1, name: 'Заказы', exact: true })).toBeVisible();
  await expect(page.getByLabel('Тема оформления', { exact: true })).toHaveValue('system');
  await expect(page.getByRole('button', { name: 'Выйти из аккаунта', exact: true })).toBeVisible();
  await sections.getByRole('link', { name: 'MarketMesh ID', exact: true }).click();
  await expect(page.locator('.id-email')).toContainText('anna@example.ru');
  await expect(page.locator('.id-email')).toContainText('Подтверждён');
  await expect(
    page.getByRole('heading', { name: 'Сеансы и устройства', exact: true }),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Изменить данные' }).click();
  await expect(page.getByRole('textbox', { name: 'Имя', exact: true })).toHaveValue('Анна');
  await page.getByLabel('Город проживания', { exact: true }).fill('<script>не HTML</script>');
  await page.getByRole('button', { name: 'Сохранить', exact: true }).click();
  await expect(page.getByText('Данные сохранены.', { exact: true })).toBeVisible();
  expect(api.updates()).toBe(1);
  await page.reload();
  await expect(page.locator('.id-rows').first()).toContainText('<script>не HTML</script>');
  expect(await page.locator('script:not([src])').count()).toBe(0);
  const storage = await page.evaluate(() => JSON.stringify({ ...localStorage }));
  expect(storage).not.toContain('Анна');
  expect(storage).not.toContain('anna@example.ru');
  expect(storage).not.toContain('password');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('id-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 900, height: 900 });
  expect(
    await page
      .locator('.account-sidebar')
      .evaluate((element) => element.getBoundingClientRect().width),
  ).toBe(230);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('id-mobile.png'), fullPage: true });
  await page.setViewportSize({ width: 320, height: 800 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});

test('login with the emailed code: wrong code, resend, success and accessibility', async ({
  page,
  context,
}) => {
  const api = await browserApi(context);
  api.enableCodeFlow();
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('Secret123!');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Подтвердите вход.' })).toBeVisible();
  await expect(page.getByText('Мы отправили код из шести цифр на anna@example.ru.')).toBeVisible();
  const code = page.getByLabel('Код из письма', { exact: true });
  await expect(code).toHaveAttribute('inputmode', 'numeric');
  await expect(code).toHaveAttribute('autocomplete', 'one-time-code');
  await code.fill('000000');
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(
    page.getByText('Код неверный. Проверьте письмо и введите код ещё раз.'),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Отправить код ещё раз' }).click();
  await expect(page.getByText('Новый код отправлен на почту.')).toBeVisible();
  await expect(code).toHaveValue('');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await code.fill('482913');
  await page.getByRole('button', { name: 'Подтвердить вход', exact: true }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Заказы', exact: true })).toBeVisible();
});

test('logout in another tab clears an unsaved private draft', async ({ page, context }) => {
  await browserApi(context);
  await buyerLogin(page);
  await openIdDraft(page, 'Несохранённый личный черновик');
  const second = await context.newPage();
  await second.goto('/account');
  await expect(
    second.getByRole('heading', { level: 1, name: 'Заказы', exact: true }),
  ).toBeVisible();
  await second.getByRole('button', { name: 'Выйти из аккаунта', exact: true }).click();
  await expect(second).toHaveURL(/\/login$/);
  await expect(page.getByLabel('Город проживания', { exact: true })).toHaveCount(0);
  await expect(page.getByText('Перейти ко входу')).toBeVisible();
  expect(await page.locator('body').textContent()).not.toContain('Несохранённый личный черновик');
});

test('an active refresh is shared by new tabs and preserves an existing draft', async ({
  page,
  context,
}) => {
  const api = await browserApi(context);
  await buyerLogin(page);
  await openIdDraft(page, 'Черновик переживёт обновление сессии');
  const release = api.expireAndHoldRefresh();
  try {
    const second = await context.newPage();
    await second.goto('/account/id');
    await expect.poll(api.refreshes).toBe(1);
    const third = await context.newPage();
    await third.goto('/account/id');
    await expect(
      third.getByText('Результат операции с сессией неизвестен.', { exact: false }),
    ).toHaveCount(0);
    release();
    await expect(second.locator('.id-hero h2')).toHaveText('Анна');
    await expect(third.locator('.id-hero h2')).toHaveText('Анна');
    await expect(page.getByLabel('Город проживания', { exact: true })).toHaveValue(
      'Черновик переживёт обновление сессии',
    );
    expect(api.refreshes()).toBe(1);
  } finally {
    release();
  }
});

async function addressLogin(page: import('@playwright/test').Page) {
  await page.goto('/login');
  await page.getByLabel('Почта', { exact: true }).fill('anna@example.ru');
  await page.getByLabel('Пароль', { exact: true }).fill('a secure demo password');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  await page.getByRole('link', { name: 'Адреса доставки', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Добавить адрес' })).toBeEnabled();
}
async function fillAddress(page: import('@playwright/test').Page, name: string) {
  await page.getByRole('textbox', { name: 'Получатель', exact: true }).fill(name);
  await page
    .getByRole('textbox', { name: 'Телефон получателя', exact: true })
    .fill('+7 999 1234567');
  await page.getByRole('textbox', { name: 'Страна', exact: true }).fill('Россия');
  await page.getByRole('textbox', { name: 'Город / населённый пункт', exact: true }).fill('Москва');
  await page.getByRole('textbox', { name: 'Улица и дом', exact: true }).fill('Улица Мира, дом 1');
}
test('address book CRUD, default, reload, accessible mobile form and lost creation reply', async ({
  page,
  context,
}, testInfo) => {
  const api = await browserApi(context);
  await addressLogin(page);
  await page.getByRole('button', { name: 'Добавить адрес' }).click();
  await fillAddress(page, 'Анна');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
  await expect(page.locator('.address-row')).toHaveCount(1);
  await page.getByRole('button', { name: 'Добавить адрес' }).click();
  await fillAddress(page, 'Борис');
  api.loseNextCreateReply();
  await page.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
  await expect(page.getByText('Новый адрес мог уже сохраниться.', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: 'Перечитать актуальные данные' }).click();
  await expect(page.locator('.address-row')).toHaveCount(2);
  await page.getByRole('button', { name: 'Принять актуальную книгу' }).click();
  expect(api.addressWrites()).toBe(2);
  const second = page.locator('.address-row').filter({ hasText: 'Борис' });
  await second.getByRole('button', { name: 'Использовать по умолчанию', exact: false }).click();
  await expect(second).toContainText('По умолчанию');
  await second.getByRole('button', { name: 'Изменить', exact: false }).click();
  await page.getByLabel('Комментарий', { exact: true }).fill('<img src=x onerror=alert(1)>');
  await page.getByRole('button', { name: 'Сохранить адрес', exact: true }).click();
  await expect(second).toContainText('<img src=x onerror=alert(1)>');
  expect(await page.locator('img').count()).toBe(0);
  await page.reload();
  await expect(page.locator('.address-row')).toHaveCount(2);
  await second.getByRole('button', { name: 'Удалить', exact: false }).click();
  const dialog = page.getByRole('dialog', { name: 'Удалить адрес «Борис»?' });
  await expect(dialog.getByRole('button', { name: 'Оставить адрес' })).toBeFocused();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(second.getByRole('button', { name: 'Удалить', exact: false })).toBeFocused();
  await second.getByRole('button', { name: 'Удалить', exact: false }).click();
  await dialog.getByRole('button', { name: 'Удалить адрес', exact: true }).click();
  await expect(page.locator('.address-row')).toHaveCount(1);
  await expect(page.getByText('По умолчанию', { exact: true })).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Добавить адрес' }).click();
  await fillAddress(page, 'Частный черновик');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({ path: testInfo.outputPath('addresses-mobile.png'), fullPage: true });
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage })),
  ).not.toContain('Частный черновик');
  const other = await context.newPage();
  await other.goto('/account');
  await expect(other.getByRole('heading', { level: 1, name: 'Заказы', exact: true })).toBeVisible();
  await other.getByRole('button', { name: 'Выйти из аккаунта', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Получатель', exact: true })).toHaveCount(0);
  await expect(page.locator('.address-row')).toHaveCount(0);
});

for (const path of ['/login', '/register']) {
  test(`credentials wait for an initial browser session lock on ${path}`, async ({
    page,
    context,
  }) => {
    await browserApi(context);
    // Acquire the real same-origin Web Lock before the app's bootstrap can do so.
    await page.addInitScript(() => {
      void navigator.locks.request(
        'marketmesh:browser-session:v1',
        { mode: 'exclusive' },
        () =>
          new Promise<void>((resolve) => {
            window.addEventListener('marketmesh-test-release-bootstrap', () => resolve(), {
              once: true,
            });
          }),
      );
    });
    await page.goto(path);
    await expect(page.getByLabel('Почта', { exact: true })).toBeDisabled();
    await expect(page.getByLabel('Пароль', { exact: true })).toBeDisabled();
    await expect(
      page.getByText('Проверяем сессию перед вводом данных…', { exact: true }),
    ).toBeVisible();
    await page.evaluate(() => window.dispatchEvent(new Event('marketmesh-test-release-bootstrap')));
    await expect(page.getByLabel('Почта', { exact: true })).toBeEnabled();
    await page.getByLabel('Почта', { exact: true }).fill('anna@example.ru');
    await page.getByLabel('Пароль', { exact: true }).fill('Secret123!');
    if (path === '/register') {
      await page.getByLabel('Повторите пароль', { exact: true }).fill('Secret123!');
      await page.getByRole('button', { name: 'Зарегистрироваться', exact: true }).click();
      await expect(page.getByText('Аккаунт создан.')).toBeVisible();
    } else {
      await page.getByRole('button', { name: 'Войти', exact: true }).click();
      await expect(
        page.getByRole('heading', { level: 1, name: 'Заказы', exact: true }),
      ).toBeVisible();
    }
  });
}

test('the sidebar theme saves on choice, follows the device, survives reload and remains accessible', async ({
  page,
  context,
}, testInfo) => {
  const api = await browserApi(context);
  await page.emulateMedia({ colorScheme: 'light' });
  await addressLogin(page);
  const theme = page.getByLabel('Тема оформления', { exact: true });
  await expect(theme).toHaveValue('system');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await theme.selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(
    page.getByText('Тема сохранена и применится на всех ваших устройствах.', { exact: true }),
  ).toBeVisible();
  expect(api.settingsWrites()).toBe(1);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.locator('main').focus();
  await page.evaluate(() => window.scrollTo(0, 0));
  expect(
    await page.locator('.skip-link').evaluate((element) => getComputedStyle(element).top),
  ).toBe('-100px');
  await page.screenshot({ path: testInfo.outputPath('theme-dark.png'), fullPage: true });
  await page.getByRole('link', { name: 'MarketMesh ID', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Личные данные', exact: true })).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(theme).toHaveValue('dark');
  await page.getByRole('link', { name: 'Адреса доставки', exact: true }).click();
  await page.getByRole('button', { name: 'Добавить адрес', exact: true }).click();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await theme.selectOption('light');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await theme.selectOption('system');
  await expect(page.locator('html')).toHaveAttribute('data-theme-preference', 'system');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  expect(api.settingsWrites()).toBe(3);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.locator('main').focus();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: testInfo.outputPath('theme-light-mobile.png'), fullPage: true });
  await theme.selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.locator('main').focus();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: testInfo.outputPath('theme-dark-mobile.png'), fullPage: true });
});

test('theme conflicts and lost replies are reread instead of retried, and cross-tab logout resets the shell', async ({
  page,
  context,
}) => {
  const api = await browserApi(context);
  await page.emulateMedia({ colorScheme: 'light' });
  await addressLogin(page);
  const second = await context.newPage();
  await second.goto('/account/settings');
  await expect(second).toHaveURL(/\/account\/orders$/);
  const pageTheme = page.getByLabel('Тема оформления', { exact: true });
  const secondTheme = second.getByLabel('Тема оформления', { exact: true });
  await expect(secondTheme).toHaveValue('system');
  await pageTheme.selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  // The second tab still holds the first version: the write conflicts and is not retried.
  const writes = api.settingsWrites();
  await secondTheme.selectOption('light');
  await expect(second.getByText('Тему изменили в другом окне', { exact: false })).toBeVisible();
  await expect(secondTheme).toHaveValue('dark');
  await expect(second.locator('html')).toHaveAttribute('data-theme-preference', 'dark');
  expect(api.settingsWrites()).toBe(writes + 1);
  await secondTheme.selectOption('light');
  await expect(second.locator('html')).toHaveAttribute('data-theme-preference', 'light');
  expect(api.settingsWrites()).toBe(writes + 2);
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  // The write lands but its reply is lost: the reread confirms it without a second write.
  api.loseNextSettingsReply();
  const lost = api.settingsWrites();
  await pageTheme.selectOption('dark');
  await expect(
    page.getByText('Тема сохранена и применится на всех ваших устройствах.', { exact: true }),
  ).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('data-theme-preference', 'dark');
  expect(api.settingsWrites()).toBe(lost + 1);
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage })),
  ).not.toMatch(/theme|dark|light/);
  await second.getByRole('button', { name: 'Выйти из аккаунта', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme-preference', 'system');
  await expect(page.getByLabel('Тема оформления', { exact: true })).toHaveCount(0);
});

test('orders section lists orders, filters them and reveals the pickup code accessibly', async ({
  page,
  context,
}) => {
  await browserApi(context);
  await buyerLogin(page);
  await page.getByRole('link', { name: /^Заказы\s*, всего 4$/ }).click();
  await expect(page.getByRole('heading', { name: 'Заказ № 1482-0091' })).toBeVisible();
  expect(page.getByText('481 902')).toHaveCount(0);
  await page.getByRole('button', { name: 'Показать код' }).click();
  await expect(page.getByText('481 902')).toBeVisible();
  await page.getByRole('button', { name: 'Отменённые', exact: true }).click();
  await expect(page.locator('.sample-card')).toHaveCount(1);
  await expect(page.getByRole('heading', { name: 'Заказ № 1388-0064' })).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});

test('favorites section filters by stock and asks for restock mail accessibly', async ({
  page,
  context,
}) => {
  await browserApi(context);
  await buyerLogin(page);
  await page.getByRole('link', { name: /^Избранное\s*, всего 6$/ }).click();
  await expect(page.locator('.favorite-card')).toHaveCount(6);
  await page.getByRole('button', { name: 'Закончились', exact: true }).click();
  await expect(page.locator('.favorite-card')).toHaveCount(2);
  await page
    .getByRole('button', { name: /Сообщить о пополнении\s*:?\s*Свеча «Хвоя и дым», 200 мл/ })
    .click();
  await expect(page.getByText('Напишем на почту, когда мастер пополнит партию.')).toBeVisible();
  await page
    .getByRole('button', { name: /Убрать из избранного\s*:?\s*Деревянная доска из дуба/ })
    .click();
  await expect(page.locator('.favorite-card')).toHaveCount(1);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test('reviews section validates the form and publishes to my reviews accessibly', async ({
  page,
  context,
}) => {
  await browserApi(context);
  await buyerLogin(page);
  await page.getByRole('link', { name: /^Отзывы\s*, ждут отзыва: 2$/ }).click();
  await page.getByRole('button', { name: 'Написать отзыв', exact: false }).first().click();
  await page.getByRole('button', { name: 'Отправить отзыв' }).click();
  await expect(page.getByText('Поставьте оценку — без неё отзыв не отправить.')).toBeVisible();
  await page.locator('label[for="rate-w1-4"]').click();
  await page
    .getByLabel('Что скажете об изделии', { exact: true })
    .fill('Тёплый плед, ровные швы, цвет как на фотографии.');
  await page.getByRole('button', { name: 'Отправить отзыв' }).click();
  await expect(page.getByText('Отзыв отправлен.', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: /Мои отзывы/ }).click();
  await expect(page.getByText('На модерации', { exact: true })).toHaveCount(2);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});

test('MarketMesh ID edits personal data with CAS and hides private fields from the signature', async ({
  page,
  context,
}) => {
  const api = await browserApi(context);
  await buyerLogin(page);
  await page.getByRole('link', { name: 'MarketMesh ID', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Личные данные', exact: true })).toBeVisible();
  await expect(page.locator('.id-rows').first()).toContainText('Не указана');
  await page.getByRole('button', { name: 'Изменить данные' }).click();
  await page.getByLabel('Телефон', { exact: true }).fill('123');
  await page.getByRole('button', { name: 'Сохранить', exact: true }).click();
  await expect(page.getByText('Номер: от 7 до 15 цифр', { exact: false })).toBeVisible();
  expect(api.updates()).toBe(0);
  await page.getByLabel('Телефон', { exact: true }).fill('+7 999 1234567');
  await page.getByLabel('Город проживания', { exact: true }).fill('Москва');
  await page.getByRole('button', { name: 'Сохранить', exact: true }).click();
  await expect(page.getByText('Данные сохранены.', { exact: true })).toBeVisible();
  expect(api.updates()).toBe(1);
  await expect(page.locator('.id-rows').first()).toContainText('Москва');
  await expect(page.locator('.review-preview')).toContainText('Анна, Москва');
  await expect(page.locator('.review-preview')).not.toContainText('+7 999 1234567');
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});
