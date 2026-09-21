import type { RouteRecordRaw } from 'vue-router';
import AuthView from './views/AuthView.vue';
import SettingsView from './views/SettingsView.vue';
import AddressesView from './views/AddressesView.vue';
import ProfileView from './views/ProfileView.vue';
import { addressesEnabled, settingsEnabled } from '../../shared/features';

/** Маршруты области покупателя; композицию выполняет shell/router. */
export const accountRoutes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: AuthView,
    props: { mode: 'login' },
    meta: { area: 'buyer' },
  },
  {
    path: '/register',
    name: 'register',
    component: AuthView,
    props: { mode: 'register' },
    meta: { area: 'buyer' },
  },
  ...(addressesEnabled
    ? [
        {
          path: '/account/addresses',
          name: 'addresses',
          component: AddressesView,
          meta: { area: 'buyer' as const },
        },
      ]
    : []),
  ...(settingsEnabled
    ? [
        {
          path: '/account/settings',
          name: 'settings',
          component: SettingsView,
          meta: { area: 'buyer' as const },
        },
      ]
    : []),
  { path: '/account', name: 'account', component: ProfileView, meta: { area: 'buyer' } },
];
