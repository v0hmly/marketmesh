import { inject, type InjectionKey } from "vue";
import type { createRouteRecovery } from "@marketmesh/browser-client/recovery";
export const recoveryKey: InjectionKey<ReturnType<typeof createRouteRecovery>> =
  Symbol("route-recovery");
export function useRouteRecovery() {
  return inject(recoveryKey, null);
}
