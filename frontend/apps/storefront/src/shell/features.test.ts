import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { createMemoryHistory } from 'vue-router';
beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {});
});
afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});
it.each([undefined, 'false', '1', 'true'])(
  'enables the address route only for the exact build flag %s',
  async (flag) => {
    vi.stubEnv('VITE_ACCOUNT_ADDRESSES_ENABLED', flag);
    vi.resetModules();
    const { addressesEnabled } = await import('../shared/features');
    const { createStorefrontRouter } = await import('./router');
    expect(addressesEnabled).toBe(flag === 'true');
    expect(
      createStorefrontRouter(createMemoryHistory())
        .getRoutes()
        .some((route) => route.path === '/account/addresses'),
    ).toBe(flag === 'true');
  },
);

it.each([undefined, 'false', '1', 'true'])(
  'keeps settings independent of the address gate for %s',
  async (flag) => {
    vi.stubEnv('VITE_ACCOUNT_SETTINGS_ENABLED', flag);
    vi.stubEnv('VITE_ACCOUNT_ADDRESSES_ENABLED', 'false');
    vi.resetModules();
    const { settingsEnabled, addressesEnabled } = await import('../shared/features');
    const { createStorefrontRouter } = await import('./router');
    expect(settingsEnabled).toBe(flag === 'true');
    expect(addressesEnabled).toBe(false);
    const router = createStorefrontRouter(createMemoryHistory());
    expect(router.getRoutes().some((route) => route.path === '/account/addresses')).toBe(false);
    // Тема выбирается в панели кабинета; прежний адрес экрана ведёт в кабинет при любом флаге.
    await router.push('/account/settings');
    expect(router.currentRoute.value.path).toBe('/account/security');
  },
);

it.each([
  [{}, '/account/security'],
  [{ VITE_ACCOUNT_ID_ENABLED: 'true' }, '/account/id'],
  [{ VITE_ACCOUNT_ID_ENABLED: 'true', VITE_ACCOUNT_ORDERS_ENABLED: 'true' }, '/account/orders'],
] as const)('opens the first enabled cabinet section for %o', async (flags, home) => {
  for (const [name, value] of Object.entries(flags)) vi.stubEnv(name, value);
  vi.resetModules();
  const { createStorefrontRouter } = await import('./router');
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push('/account');
  expect(router.currentRoute.value.path).toBe(home);
  await router.push('/account/security');
  expect(router.currentRoute.value.fullPath).toBe(
    'VITE_ACCOUNT_ID_ENABLED' in flags ? '/account/id#security' : '/account/security',
  );
});

it.each([undefined, 'false', '1', 'true'])(
  'enables the seller portal routes only for the exact build flag %s',
  async (flag) => {
    vi.stubEnv('VITE_SELLER_ENABLED', flag);
    vi.resetModules();
    const { sellerEnabled } = await import('../shared/features');
    const { createStorefrontRouter } = await import('./router');
    expect(sellerEnabled).toBe(flag === 'true');
    const routes = createStorefrontRouter(createMemoryHistory()).getRoutes();
    expect(routes.some((route) => route.path === '/seller/login')).toBe(flag === 'true');
    expect(routes.some((route) => route.path === '/seller')).toBe(flag === 'true');
  },
);

it.each([undefined, 'false', 'true'])(
  'never exposes staff routes in storefront (%s)',
  async (flag) => {
    vi.stubEnv('VITE_STAFF_ENABLED', flag);
    vi.resetModules();
    const { createStorefrontRouter } = await import('./router');
    expect(
      createStorefrontRouter(createMemoryHistory())
        .getRoutes()
        .some((route) => route.path.startsWith('/staff')),
    ).toBe(false);
  },
);
