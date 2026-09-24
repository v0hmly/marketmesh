import { afterEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import { createRouteRecovery } from '@marketmesh/browser-client/recovery';

afterEach(() => vi.restoreAllMocks());
describe('lazy route recovery', () => {
  it('keeps a rejected route recoverable without replaying navigation or mutations', async () => {
    let calls = 0;
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<p>Home</p>' } },
        {
          path: '/seller',
          component: () => {
            calls++;
            return Promise.reject(new Error('chunk unavailable'));
          },
        },
      ],
    });
    const reload = vi.fn();
    const replaceState = vi.fn();
    const target = new EventTarget() as Window;
    Object.defineProperty(target, 'location', {
      value: { origin: 'https://marketmesh.test', reload },
    });
    Object.defineProperty(target, 'history', { value: { replaceState } });
    const recovery = createRouteRecovery(router, target);
    await router.push('/');
    await expect(router.push('/seller')).rejects.toThrow('chunk unavailable');
    expect(recovery.failedPath.value).toBe('/seller');
    expect(reload).not.toHaveBeenCalled();
    expect(calls).toBe(1);
    recovery.reload();
    expect(replaceState).toHaveBeenCalledExactlyOnceWith(
      null,
      '',
      'https://marketmesh.test/seller',
    );
    expect(reload).toHaveBeenCalledExactlyOnceWith();
    await router.push('/');
    // Repeating the current route is a navigation failure; the recovery action remains available.
    expect(recovery.failedPath.value).toBe('/seller');
    recovery.dispose();
  });
  it('handles a stale preload once and removes the listener on dispose', () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    const target = new EventTarget() as Window;
    const recovery = createRouteRecovery(router, target);
    const event = new Event('vite:preloadError', { cancelable: true });
    target.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(recovery.failedPath.value).toBe('/');
    recovery.dispose();
    const after = new Event('vite:preloadError', { cancelable: true });
    target.dispatchEvent(after);
    expect(after.defaultPrevented).toBe(false);
  });
});
