import type { RouteRecordRaw } from 'vue-router';
import { homeEnabled } from '../../shared/features';
const HomeView = () => import('./views/HomeView.vue');
const StorefrontHeader = () => import('./components/StorefrontHeader.vue');

/**
 * Маршруты витрины; композицию выполняет shell/router. Без флага главная
 * по-прежнему ведёт в кабинет. Шапка — именованный вид `header` в shell.
 */
export const catalogRoutes: RouteRecordRaw[] = homeEnabled
  ? [
      {
        path: '/',
        name: 'home',
        components: { default: HomeView, header: StorefrontHeader },
        meta: { area: 'buyer', section: 'Витрина', storefront: true },
      },
    ]
  : [];
