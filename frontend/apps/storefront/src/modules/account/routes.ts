import type { RouteRecordRaw } from 'vue-router';
import AuthView from './views/AuthView.vue';
import SecurityView from './views/SecurityView.vue';
import SecurityLinkView from './views/SecurityLinkView.vue';
import SettingsView from './views/SettingsView.vue';
import AddressesView from './views/AddressesView.vue';
import OrdersView from './views/OrdersView.vue';
import FavoritesView from './views/FavoritesView.vue';
import ReviewsView from './views/ReviewsView.vue';
import IdView from './views/IdView.vue';
import ProfileView from './views/ProfileView.vue';
import {
  addressesEnabled,
  favoritesEnabled,
  idEnabled,
  ordersEnabled,
  reviewsEnabled,
  settingsEnabled,
} from '../../shared/features';

/** Маршруты области покупателя; композицию выполняет shell/router. */
export const accountRoutes: RouteRecordRaw[] = [
  { path: '/account/security', name: 'security', component: SecurityView, meta: { area: 'buyer' } },
  {
    path: '/account/security/:action(verify|reset|change_email|cancel_email)',
    name: 'security-link',
    component: SecurityLinkView,
    meta: { area: 'buyer' },
  },
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
  ...(ordersEnabled
    ? [
        {
          path: '/account/orders',
          name: 'orders',
          component: OrdersView,
          meta: { area: 'buyer' as const },
        },
      ]
    : []),
  ...(favoritesEnabled
    ? [
        {
          path: '/account/favorites',
          name: 'favorites',
          component: FavoritesView,
          meta: { area: 'buyer' as const },
        },
      ]
    : []),
  ...(reviewsEnabled
    ? [
        {
          path: '/account/reviews',
          name: 'reviews',
          component: ReviewsView,
          meta: { area: 'buyer' as const },
        },
      ]
    : []),
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
  ...(idEnabled
    ? [
        {
          path: '/account/id',
          name: 'id',
          component: IdView,
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
