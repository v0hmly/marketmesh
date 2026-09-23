import type { RouteRecordRaw } from 'vue-router';
import AuthView from './views/AuthView.vue';
import AccountLayout from './views/AccountLayout.vue';
import SecurityView from './views/SecurityView.vue';
import SecurityLinkView from './views/SecurityLinkView.vue';
import AddressesView from './views/AddressesView.vue';
import OrdersView from './views/OrdersView.vue';
import FavoritesView from './views/FavoritesView.vue';
import ReviewsView from './views/ReviewsView.vue';
import IdView from './views/IdView.vue';
import {
  addressesEnabled,
  favoritesEnabled,
  idEnabled,
  ordersEnabled,
  reviewsEnabled,
} from '../../shared/features';

/**
 * Первый доступный раздел кабинета. Без MarketMesh ID (сборка до выкатки нового User)
 * вход и безопасность остаются отдельным разделом.
 */
export const accountHomePath = ordersEnabled
  ? '/account/orders'
  : idEnabled
    ? '/account/id'
    : '/account/security';

const sections: RouteRecordRaw[] = [
  ...(ordersEnabled ? [{ path: 'orders', name: 'orders', component: OrdersView }] : []),
  ...(favoritesEnabled ? [{ path: 'favorites', name: 'favorites', component: FavoritesView }] : []),
  ...(reviewsEnabled ? [{ path: 'reviews', name: 'reviews', component: ReviewsView }] : []),
  ...(addressesEnabled ? [{ path: 'addresses', name: 'addresses', component: AddressesView }] : []),
  ...(idEnabled
    ? [
        { path: 'id', name: 'id', component: IdView },
        // Безопасность — раздел экрана MarketMesh ID.
        {
          path: 'security',
          name: 'security',
          redirect: { path: '/account/id', hash: '#security' },
        },
      ]
    : [{ path: 'security', name: 'security', component: SecurityView }]),
];

/** Маршруты области покупателя; композицию выполняет shell/router. */
export const accountRoutes: RouteRecordRaw[] = [
  {
    path: '/account/security/:action(verify|reset|change_email|cancel_email)',
    name: 'security-link',
    component: SecurityLinkView,
    meta: { area: 'buyer', section: 'Безопасность аккаунта' },
  },
  {
    path: '/login',
    name: 'login',
    component: AuthView,
    props: { mode: 'login' },
    meta: { area: 'buyer', section: 'Вход' },
  },
  {
    path: '/register',
    name: 'register',
    component: AuthView,
    props: { mode: 'register' },
    meta: { area: 'buyer', section: 'Регистрация' },
  },
  {
    path: '/account',
    component: AccountLayout,
    meta: { area: 'buyer' },
    children: [
      { path: '', name: 'account', redirect: accountHomePath },
      ...sections,
      // Тема оформления теперь в панели кабинета, отдельного экрана нет.
      { path: 'settings', redirect: '/account' },
    ],
  },
];
