import { inject, type Ref, type InjectionKey } from "vue";
import type { StaffApi } from "./api";
export const staffApiKey: InjectionKey<StaffApi> = Symbol("marketmesh-staff");
export function useStaffApi(): StaffApi {
  const value = inject(staffApiKey);
  if (!value) throw new Error("staff controller is missing");
  return value;
}

export const invalidatedKey: InjectionKey<Readonly<Ref<boolean>>> =
  Symbol("staff-invalidated");
