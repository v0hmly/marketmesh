import type { RouteRecordRaw } from 'vue-router';
import { sellerEnabled } from '../../shared/features';
const SellerLoginView = () => import('./views/SellerLoginView.vue');
const SellerApplyView = () => import('./views/SellerApplyView.vue');
const SellerDashboardView = () => import('./views/SellerDashboardView.vue');
const SellerProductsView = () => import('./views/SellerProductsView.vue');
const SellerOrdersView = () => import('./views/SellerOrdersView.vue');

/** Маршруты области продавца; композицию выполняет shell/router. */
export const sellerRoutes: RouteRecordRaw[] = sellerEnabled
  ? [
      {
        path: '/seller/login',
        name: 'seller-login',
        component: SellerLoginView,
        meta: { area: 'seller' },
      },
      {
        path: '/seller/apply',
        name: 'seller-apply',
        component: SellerApplyView,
        meta: { area: 'seller' },
      },
      {
        path: '/seller',
        name: 'seller-dashboard',
        component: SellerDashboardView,
        meta: { area: 'seller' },
      },
      {
        path: '/seller/products',
        name: 'seller-products',
        component: SellerProductsView,
        meta: { area: 'seller' },
      },
      {
        path: '/seller/orders',
        name: 'seller-orders',
        component: SellerOrdersView,
        meta: { area: 'seller' },
      },
    ]
  : [];
