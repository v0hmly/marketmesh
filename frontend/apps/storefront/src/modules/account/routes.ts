import type { RouteRecordRaw } from 'vue-router';
const AccountLayout = () => import('./views/AccountLayout.vue');
const SecurityView = () => import('./views/SecurityView.vue');
const AddressesView = () => import('./addresses/AddressesView.vue');
const OrdersView = () => import('./orders/views/OrdersView.vue');
const FavoritesView = () => import('./favorites/views/FavoritesView.vue');
const ReviewsView = () => import('./reviews/views/ReviewsView.vue');
const IdView = () => import('./profile/IdView.vue');
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
