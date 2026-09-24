import { createRouter, createWebHistory, type RouterHistory } from 'vue-router';
import { authRoutes } from '../modules/auth/routes';
import { accountRoutes } from '../modules/account/routes';
import { sellerRoutes } from '../modules/seller/routes';

/**
 * Композиция маршрутов продуктовых областей (ADR-0010): shell собирает
 * маршруты модулей, владеет историей и скроллом; модуль не импортирует
 * маршруты других модулей.
 */
export function createStorefrontRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({
    history,
    routes: [
      { path: '/', redirect: '/account' },
      ...authRoutes,
      ...accountRoutes,
      ...sellerRoutes,
      { path: '/:pathMatch(.*)*', redirect: '/account' },
    ],
    // Якорь-идентификатор (например, /account/id#security) ведёт к разделу, если он уже
    // на странице; остальное — к началу. Hash ссылок из писем (#token=…) не селектор.
    scrollBehavior: (to) =>
      /^#[A-Za-z][\w-]*$/.test(to.hash) && document.getElementById(to.hash.slice(1))
        ? { el: to.hash, top: 24 }
        : { top: 0 },
  });
}
