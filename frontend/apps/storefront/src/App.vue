<script setup lang="ts">
import { useRouteRecovery } from './recovery';
const recovery = useRouteRecovery();
import BrandMark from '@marketmesh/design-system/BrandMark.vue';
import { useAccount } from './modules/account/api/controller';
import { Code } from '@connectrpc/connect';
import { authErrorReason } from './modules/auth/public';
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { settingsEnabled } from './shared/features';
import { createThemeController } from './modules/account/settings/theme';
import { areaTitle } from './shell/areas';
import { useSession } from './shell/context';
import { themeKey } from './modules/account/settings/context';

const session = useSession();
const theme = settingsEnabled ? createThemeController(session, useAccount()) : null;
if (theme) provide(themeKey, theme);
onBeforeUnmount(() => theme?.dispose());
const route = useRoute();
const router = useRouter();
const logoutFailure = ref('');
const busy = ref(false);
watch(
  () => session.state.value.generation,
  () => {
    logoutFailure.value = '';
  },
);
/** Кабинет покупателя выводит выход в своей боковой панели (MM-97). */
const inAccountLayout = computed(() => route.matched[0]?.path === '/account');
/** Подвал называет текущее место: раздел маршрута или его область. */
const footerSection = computed(
  () => route.meta.section ?? (route.meta.area ? areaTitle[route.meta.area] : 'MarketMesh'),
);
const signedIn = computed(() =>
  ['authenticated', 'profilePending'].includes(session.state.value.status),
);
const stateMessage = computed(() => {
  switch (session.state.value.status) {
    case 'unsupported':
      return 'Этот браузер не поддерживает безопасную координацию сессии. Откройте MarketMesh в поддерживаемом браузере.';
    case 'uncertain':
      return 'Результат операции с сессией неизвестен. Автоматический повтор остановлен. Войдите заново.';
    case 'unavailable':
      return 'Не удалось проверить сессию. Проверьте соединение и повторите попытку.';
    default:
      return '';
  }
});

async function bootstrap() {
  busy.value = true;
  try {
    await session.bootstrap();
  } catch {
    /* State owns safe failure presentation. */
  } finally {
    busy.value = false;
  }
}
// Общие кнопки выхода остаются для областей без своего каркаса (портал продавца).
async function logout(all: boolean) {
  if (busy.value) return;
  busy.value = true;
  logoutFailure.value = '';
  try {
    await session.logout(all);
    await router.push('/login');
  } catch (error) {
    logoutFailure.value = authErrorReason(error, Code.FailedPrecondition, 'NEW_DEVICE_COOLDOWN')
      ? 'После нового входа другие сеансы защищены на 24 часа. Выход не выполнен. Текущий сеанс можно закрыть кнопкой «Выйти».'
      : 'Сервер не подтвердил выход. Сессия может оставаться активной. Проверьте соединение.';
  } finally {
    busy.value = false;
  }
}
onMounted(() => {
  void bootstrap();
});
</script>

<template>
  <a class="skip-link" href="#main-content">Перейти к содержимому</a>
  <div class="site-wrap">
    <header class="site-header">
      <RouterLink class="brand" to="/account" aria-label="MarketMesh — личный кабинет"
        ><BrandMark /><span>MarketMesh<span class="brand-dot">.</span></span></RouterLink
      >
      <span class="brand-caption">Изделия мастеров. Личные истории.</span>
      <nav class="header-nav" aria-label="Основная навигация">
        <RouterLink v-if="signedIn" class="nav-link" to="/account">Личный кабинет</RouterLink>
        <template v-else
          ><RouterLink class="nav-link" to="/login">Вход</RouterLink
          ><RouterLink class="nav-link" to="/register">Регистрация</RouterLink></template
        >
      </nav>
    </header>
    <main id="main-content" tabindex="-1">
      <div v-if="stateMessage" class="notice error session-notice" role="status">
        <p>{{ stateMessage }}</p>
        <button
          v-if="session.state.value.status === 'unavailable'"
          class="button secondary"
          :disabled="busy"
          @click="bootstrap"
        >
          {{ busy ? 'Проверяем…' : 'Проверить соединение' }}</button
        ><RouterLink v-if="session.state.value.status === 'uncertain'" to="/login"
          >Войти заново</RouterLink
        >
      </div>
      <p v-if="logoutFailure" class="notice error session-notice" role="alert">
        {{ logoutFailure }}
      </p>
      <div v-if="theme?.failed.value && signedIn" class="notice error session-notice" role="status">
        <p>Не удалось загрузить оформление аккаунта.</p>
        <button class="button secondary" @click="theme.load()">
          Повторить загрузку оформления
        </button>
      </div>
      <section v-if="recovery?.failedPath.value" class="card state-card" role="alert">
        <h1 class="state-title">Не удалось открыть раздел.</h1>
        <p>
          Возможно, сайт обновился или пропало соединение. Обновите страницу, чтобы продолжить.
          Несохранённые изменения будут потеряны.
        </p>
        <button class="button secondary" @click="recovery.reload()">Обновить страницу</button>
      </section>
      <RouterView v-else />
      <section
        v-if="signedIn && !inAccountLayout"
        class="session-controls"
        aria-label="Управление сессией"
      >
        <p>Закончили на этом устройстве?</p>
        <div class="button-row">
          <button class="button text-button" :disabled="busy" @click="logout(false)">Выйти</button
          ><button
            class="button text-button"
            :disabled="busy"
            aria-describedby="session-controls-all-help"
            @click="logout(true)"
          >
            Выйти на всех устройствах
          </button>
        </div>
        <p id="session-controls-all-help">Выход на всех устройствах закроет и этот сеанс.</p>
      </section>
    </main>
    <footer class="site-footer">
      <span>MarketMesh</span><span>Сделано людьми. Для людей.</span
      ><span class="footer-meta">{{ footerSection }}</span>
    </footer>
  </div>
</template>
