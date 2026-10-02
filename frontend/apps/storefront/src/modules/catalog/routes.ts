import type { RouteRecordRaw } from 'vue-router';
const HomeView = () => import('./views/HomeView.vue');
const StorefrontHeader = () => import('./components/StorefrontHeader.vue');

/** Маршруты витрины; композицию выполняет shell/router. Шапка — именованный вид `header`. */
export const catalogRoutes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'home',
    components: { default: HomeView, header: StorefrontHeader },
    meta: { area: 'buyer', section: 'Витрина', storefront: true },
  },
];
