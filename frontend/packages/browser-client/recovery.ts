import { shallowRef } from "vue";
import type { Router } from "vue-router";

/** A failed lazy route never reloads itself or repeats a pending form submission. */
export function createRouteRecovery(router: Router, target: Window = window) {
  const failedPath = shallowRef<string | null>(null);
  const removeError = router.onError((_error, to) => {
    failedPath.value = to.fullPath;
  });
  const removeAfter = router.afterEach((_to, _from, failure) => {
    if (!failure) failedPath.value = null;
  });
  const preload = (event: Event) => {
    event.preventDefault();
    failedPath.value ??= router.currentRoute.value.fullPath;
  };
  target.addEventListener("vite:preloadError", preload);
  return {
    failedPath,
    reload() {
      const path = failedPath.value;
      if (!path) return;
      // Never let an error payload become a redirect; only router-owned same-origin paths.
      const url = new URL(path, target.location.origin);
      if (url.origin !== target.location.origin) return;
      // assign(sameURL#fragment) is only fragment navigation and does not fetch chunks.
      target.history.replaceState(null, "", url.href);
      target.location.reload();
    },
    dispose() {
      removeError();
      removeAfter();
      target.removeEventListener("vite:preloadError", preload);
    },
  };
}
