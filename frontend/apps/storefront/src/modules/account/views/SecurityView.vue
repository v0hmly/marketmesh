<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import AccountSecurity from '../components/AccountSecurity.vue';

/** Отдельный раздел безопасности — только в сборке без MarketMesh ID. */
const session = useSession();
const status = computed(() => session.state.value.status);
const checking = ref(false);
const attempts = ref(0);
const delays = [1500, 3000, 6000, 12000];
let timer: ReturnType<typeof setTimeout> | undefined;
let active = true;

function stop() {
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
}
/** Пока профиль готовится, проверяем готовность так же, как остальные разделы. */
async function check() {
  if (checking.value || status.value !== 'profilePending') return;
  stop();
  checking.value = true;
  try {
    await session.readProfile();
  } catch {
    /* Pending state stays visible; the shell presents other failures. */
  } finally {
    checking.value = false;
    schedule();
  }
}
function schedule() {
  stop();
  if (!active || status.value !== 'profilePending' || attempts.value >= delays.length) return;
  timer = setTimeout(() => {
    timer = undefined;
    attempts.value++;
    void check();
  }, delays[attempts.value]);
}
async function retry() {
  attempts.value = 0;
  await check();
}
watch(
  status,
  (value) => {
    if (value === 'profilePending') schedule();
    else {
      stop();
      attempts.value = 0;
    }
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  stop();
});
</script>

<template>
  <section class="account-section" aria-labelledby="security-page-title">
    <div class="account-heading">
      <h1 id="security-page-title">Вход и безопасность</h1>
      <p class="lede">
        Почта и пароль для входа, подтверждение кодом и устройства, с которых вы заходили.
      </p>
    </div>
    <div v-if="status !== 'authenticated'" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(status)" role="status">Проверяем сессию…</p>
      <template v-else-if="status === 'profilePending'"
        ><h2>Готовим ваш аккаунт</h2>
        <p role="status">Вход выполнен. Настройки входа появятся после подготовки профиля.</p>
        <p v-if="attempts >= delays.length">Автоматическая проверка приостановлена.</p>
        <button class="button secondary" :disabled="checking" @click="retry">
          {{ checking ? 'Проверяем…' : 'Проверить готовность' }}
        </button></template
      >
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы управлять входом и сеансами.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <AccountSecurity />
  </section>
</template>
