import { createRouter, createWebHistory, type RouterHistory } from 'vue-router';
import { accountRoutes } from '../modules/account/routes';

/**
 * Композиция маршрутов продуктовых областей (ADR-0010): shell собирает
 * маршруты модулей, владеет историей и скроллом; модуль не импортирует
 * маршруты других модулей. Маршруты продавца и сотрудника добавляют
 * modules/seller и modules/staff (MM-84/MM-85).
 */
export function createStorefrontRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({
    history,
    routes: [
      { path: '/', redirect: '/account' },
      ...accountRoutes,
      { path: '/:pathMatch(.*)*', redirect: '/account' },
    ],
    scrollBehavior: () => ({ top: 0 }),
  });
}
