import { inject, type InjectionKey } from 'vue';
import type { ThemeController } from './theme';
export type { ConfirmedSettings } from './theme';
export const themeKey: InjectionKey<ThemeController> = Symbol('marketmesh-theme');
export function useTheme(): ThemeController {
  const value = inject(themeKey);
  if (!value) throw new Error('theme controller is missing');
  return value;
}
