import { createApp, h } from 'vue';
import App from './App.vue';
import { createPublicApi } from './shared/api/client';
import { createSessionController } from './shell/session';
import { sessionKey } from './shell/context';
import { createStorefrontRouter } from './shell/router';
import './style.css';

try {
  const session = createSessionController(createPublicApi());
  const app = createApp(App);
  app.provide(sessionKey, session);
  app.use(createStorefrontRouter());
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
