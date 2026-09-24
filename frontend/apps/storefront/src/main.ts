import { createRouteRecovery } from '@marketmesh/browser-client/recovery';
import { recoveryKey } from './recovery';
import { createApp, h } from 'vue';
import App from './App.vue';
import { createAuthApi } from './modules/auth/api';
import { createAccountApi } from './modules/account/api/client';
import { createAccountController, accountKey } from './modules/account/api/controller';
import { accountIdentityProbe } from './shell/identity';
import { createSessionController } from './shell/session';
import { sessionKey } from './shell/context';
import { createStorefrontRouter } from './shell/router';
import './style.css';
import { installAnalytics } from './shared/analytics';

try {
  const accountApi = createAccountApi();
  const session = createSessionController(createAuthApi(accountIdentityProbe(accountApi)));
  const app = createApp(App);
  app.provide(sessionKey, session);
  app.provide(accountKey, createAccountController(session, accountApi));
  const router = createStorefrontRouter();
  app.provide(recoveryKey, createRouteRecovery(router));
  installAnalytics(router);
  app.use(router);
  app.mount('#app');
} catch {
  createApp({
    render: () =>
      h('main', { class: 'site-wrap', 'aria-labelledby': 'secure-origin-title' }, [
        h('section', { class: 'card state-card' }, [
          h('p', { class: 'eyebrow' }, 'MARKETMESH'),
          h('h1', { id: 'secure-origin-title' }, 'Нужно защищённое соединение'),
          h('p', { role: 'alert' }, 'Для входа откройте защищённую версию сайта (HTTPS).'),
        ]),
      ]),
  }).mount('#app');
}
