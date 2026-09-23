import { inject, type InjectionKey } from 'vue';
import type { ThemeController } from './theme';

/** Модули видят shell только через context и session: тип подтверждённых настроек — отсюда. */
export type { ConfirmedSettings } from './theme';
import type { SessionController } from './session';
import type { SellerApi } from '../shared/api/seller';
import type { StaffApi } from '../shared/api/staff';

export const sessionKey: InjectionKey<SessionController> = Symbol('marketmesh-session');

export function useSession(): SessionController {
  const session = inject(sessionKey);
  if (!session) throw new Error('Session controller is missing');
  return session;
}

export const sellerApiKey: InjectionKey<SellerApi> = Symbol('marketmesh-seller-api');

export function useSellerApi(): SellerApi {
  const api = inject(sellerApiKey);
  if (!api) throw new Error('Seller API is missing');
  return api;
}

export const staffApiKey: InjectionKey<StaffApi> = Symbol('marketmesh-staff-api');

export function useStaffApi(): StaffApi {
  const api = inject(staffApiKey);
  if (!api) throw new Error('Staff API is missing');
  return api;
}

export const themeKey: InjectionKey<ThemeController> = Symbol('marketmesh-theme');
export function useTheme(): ThemeController {
  const theme = inject(themeKey);
  if (!theme) throw new Error('Theme controller is missing');
  return theme;
}
