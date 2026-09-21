<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { settingsEnabled } from './shared/features';
import { createThemeController } from './shell/theme';
import { useSession, themeKey } from './shell/context';

const session = useSession();
const theme = settingsEnabled ? createThemeController(session) : null;
if (theme) provide(themeKey, theme);
onBeforeUnmount(() => theme?.dispose());
const router = useRouter();
const logoutFailure = ref('');
const busy = ref(false);
watch(
  () => session.state.value.generation,
  () => {
    logoutFailure.value = '';
  },
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
async function logout(all: boolean) {
  if (busy.value) return;
  busy.value = true;
  logoutFailure.value = '';
  try {
    await session.logout(all);
    await router.push('/login');
  } catch {
    logoutFailure.value =
      'Сервер не подтвердил выход. Сессия может оставаться активной. Проверьте соединение.';
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
        ><span class="brand-mark" aria-hidden="true"><span>m</span></span
        ><span>marketmesh<span class="brand-dot">.</span></span></RouterLink
      >
      <span class="brand-caption">Изделия мастеров. Личные истории.</span>
      <nav class="header-nav" aria-label="Основная навигация">
        <RouterLink v-if="signedIn" class="nav-link" to="/account">Мой профиль</RouterLink>
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
      <RouterView />
      <section v-if="signedIn" class="session-controls" aria-label="Управление сессией">
        <p>Закончили на этом устройстве?</p>
        <div class="button-row">
          <button class="button text-button" :disabled="busy" @click="logout(false)">Выйти</button
          ><button class="button text-button" :disabled="busy" @click="logout(true)">
            Выйти на всех устройствах
          </button>
        </div>
      </section>
    </main>
    <footer class="site-footer">
      <span>MarketMesh</span><span>Сделано людьми. Для людей.</span
      ><span class="footer-meta">Личный кабинет</span>
    </footer>
  </div>
</template>
