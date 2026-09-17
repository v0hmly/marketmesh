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
