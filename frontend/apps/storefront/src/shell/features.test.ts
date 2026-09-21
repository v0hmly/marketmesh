import { afterEach, expect, it, vi } from 'vitest';
import { createMemoryHistory } from 'vue-router';
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
    const routes = createStorefrontRouter(createMemoryHistory()).getRoutes();
    expect(routes.some((route) => route.path === '/account/settings')).toBe(flag === 'true');
    expect(routes.some((route) => route.path === '/account/addresses')).toBe(false);
  },
);

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
