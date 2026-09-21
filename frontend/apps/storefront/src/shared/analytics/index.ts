import type { Router } from 'vue-router';

export type AccountEvent = 'registration_request_completed' | 'login_succeeded';
const paths = new Set([
  '/login',
  '/register',
  '/account',
  '/account/addresses',
  '/account/settings',
]);

export function createAnalytics(enabled: boolean, send: typeof fetch = globalThis.fetch) {
  let pending = 0;
  let previousPath = '';
  function track(pathname: string, event?: AccountEvent) {
    if (!enabled || navigator.doNotTrack === '1' || !paths.has(pathname) || pending >= 8) return;
    if (event && event !== 'registration_request_completed' && event !== 'login_succeeded') return;
    pending++;
    try {
      void send('/analytics/track', {
        method: 'POST',
        credentials: 'omit',
        referrerPolicy: 'no-referrer',
        redirect: 'error',
        cache: 'no-store',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          type: event ? 'custom_event' : 'pageview',
          pathname,
          ...(event ? { event_name: event } : {}),
        }),
        signal: AbortSignal.timeout(1500),
      })
        .catch(() => undefined)
        .finally(() => {
          pending--;
        });
    } catch {
      pending--;
    }
  }
  return {
    pageview(pathname: string) {
      if (previousPath === pathname) return;
      previousPath = pathname;
      track(pathname);
    },
    event(name: AccountEvent) {
      track(name === 'login_succeeded' ? '/login' : '/register', name);
    },
  };
}

export const analytics = createAnalytics(import.meta.env.VITE_RYBBIT_ENABLED === 'true');

export function installAnalytics(router: Router) {
  // afterEach includes the initial navigation. Failed/duplicated navigations and
  // query/hash changes do not create a second view of the same page.
  return router.afterEach((to, _from, failure) => {
    if (!failure) analytics.pageview(to.path);
  });
}
