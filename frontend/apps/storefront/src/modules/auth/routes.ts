import type { RouteRecordRaw } from 'vue-router';
const AuthView = () => import('./views/AuthView.vue');
const SecurityLinkView = () => import('./views/SecurityLinkView.vue');
export const authRoutes: RouteRecordRaw[] = [
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
];
