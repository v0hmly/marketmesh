import type { RouteRecordRaw } from 'vue-router';
import { sellerEnabled } from '../../shared/features';
import SellerLoginView from './views/SellerLoginView.vue';
import SellerApplyView from './views/SellerApplyView.vue';
import SellerDashboardView from './views/SellerDashboardView.vue';
import SellerProductsView from './views/SellerProductsView.vue';
import SellerOrdersView from './views/SellerOrdersView.vue';

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
