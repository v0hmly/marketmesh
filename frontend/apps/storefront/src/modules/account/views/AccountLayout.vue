<script setup lang="ts">
import '../style.css';
import { computed, inject, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useSession } from '../../../shell/context';
import { themeKey } from '../settings/context';
import { favoritesEnabled, ordersEnabled, reviewsEnabled } from '../../../shared/features';
import AccountNav from '../components/AccountNav.vue';
import ThemeSetting from '../settings/ThemeSetting.vue';
import { provideAccountCounts, type CountedSection } from '../counts';
import { loadSampleFavorites } from '../favorites/sample-data';
import { loadSampleOrders } from '../orders/sample-data';
import { loadSampleReviews } from '../reviews/sample-data';

/**
 * Каркас кабинета покупателя: слева панель (разделы, тема, выход), справа экран раздела.
 * Панель видна только владельцу сессии; экраны сами показывают вход и ожидание.
 */
const session = useSession();
const theme = inject(themeKey, null);
const router = useRouter();
const route = useRoute();
const counts = provideAccountCounts();
const leaving = ref(false);
const logoutFailure = ref('');
let countsRevision = 0;

// profilePending after a reload has no subject yet, but the session is live: keep the exit.
const signedIn = computed(() => {
  const state = session.state.value;
  return (
    state.status === 'profilePending' ||
    (Boolean(state.subjectId) && ['authenticated', 'checking'].includes(state.status))
  );
});

function clearCounts() {
  countsRevision++;
  for (const key of Object.keys(counts) as CountedSection[]) delete counts[key];
}
/**
 * Счётчики до первого открытия раздела. Пока разделы работают на образцовых данных
 * (MM-52…MM-55), читаем их же; экраны уточняют число после загрузки и изменений.
 * Открытый раздел сам сообщает своё число, поэтому его данные повторно не читаем.
 */
async function loadCounts() {
  const attempt = countsRevision;
  const needed = (section: CountedSection, enabled: boolean) =>
    enabled && route.name !== section && counts[section] === undefined;
  const settle = (section: CountedSection, value: number) => {
    if (attempt === countsRevision && counts[section] === undefined) counts[section] = value;
  };
  const tasks: Promise<void>[] = [];
  if (needed('orders', ordersEnabled))
    tasks.push(loadSampleOrders().then((value) => settle('orders', value.length)));
  if (needed('favorites', favoritesEnabled))
    tasks.push(loadSampleFavorites().then((value) => settle('favorites', value.length)));
  if (needed('reviews', reviewsEnabled))
    tasks.push(loadSampleReviews().then((value) => settle('reviews', value.waiting.length)));
  // Без счётчика пункт меню остаётся рабочим, поэтому ошибка здесь не показывается.
  await Promise.allSettled(tasks);
}
async function logout() {
  if (leaving.value) return;
  leaving.value = true;
  logoutFailure.value = '';
  try {
    await session.logout(false);
    await router.push('/login');
  } catch {
    logoutFailure.value =
      'Сервер не подтвердил выход. Сессия может оставаться активной. Проверьте соединение и попробуйте ещё раз.';
  } finally {
    leaving.value = false;
  }
}

watch(
  () => [session.state.value.generation, session.state.value.subjectId] as const,
  () => {
    clearCounts();
    logoutFailure.value = '';
  },
  { flush: 'sync' },
);
watch(
  () =>
    [
      session.state.value.generation,
      session.state.value.subjectId,
      session.state.value.status === 'authenticated',
    ] as const,
  ([, , ready]) => {
    if (ready) void loadCounts();
  },
  { immediate: true },
);
</script>

<template>
  <div class="account-layout" :class="{ 'account-layout-signed': signedIn }">
    <p v-if="logoutFailure" class="notice error session-notice account-wide" role="alert">
      {{ logoutFailure }}
    </p>
    <aside v-if="signedIn" class="account-sidebar" aria-label="Личный кабинет">
      <AccountNav :counts="counts" />
      <ThemeSetting v-if="theme" />
      <button
        type="button"
        class="button text-button account-signout"
        :disabled="leaving"
        @click="logout"
      >
        {{ leaving ? 'Выходим…' : 'Выйти из аккаунта' }}
      </button>
    </aside>
    <div class="account-main">
      <RouterView />
    </div>
  </div>
</template>
