import { inject, type InjectionKey } from 'vue';
import type { ThemeController } from './theme';
import type { SessionController } from './session';

export const sessionKey: InjectionKey<SessionController> = Symbol('marketmesh-session');

export function useSession(): SessionController {
  const session = inject(sessionKey);
  if (!session) throw new Error('Session controller is missing');
  return session;
}

export const themeKey: InjectionKey<ThemeController> = Symbol('marketmesh-theme');
export function useTheme(): ThemeController {
  const theme = inject(themeKey);
  if (!theme) throw new Error('Theme controller is missing');
  return theme;
}
