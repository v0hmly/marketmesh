import { createRouter, createWebHistory, type RouterHistory } from 'vue-router';
import AuthView from '../modules/account/views/AuthView.vue';
import ProfileView from '../modules/account/views/ProfileView.vue';

export function createStorefrontRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({
    history,
    routes: [
      { path: '/', redirect: '/account' },
      { path: '/login', name: 'login', component: AuthView, props: { mode: 'login' } },
      { path: '/register', name: 'register', component: AuthView, props: { mode: 'register' } },
      { path: '/account', name: 'account', component: ProfileView },
      { path: '/:pathMatch(.*)*', redirect: '/account' },
    ],
    scrollBehavior: () => ({ top: 0 }),
  });
}
