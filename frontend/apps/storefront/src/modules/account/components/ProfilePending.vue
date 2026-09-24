<script setup lang="ts">
import { useAccount } from '../api/controller';
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';

/**
 * Состояние «аккаунт готовится» для разделов, которым нужен готовый профиль. Вход уже
 * выполнен, поэтому звать ко входу нельзя: готовность проверяется с растущей паузой,
 * после четырёх попыток остаётся ручная проверка.
 */
defineProps<{ title: string; text: string }>();
const session = useSession();
const account = useAccount();
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
async function check() {
  if (checking.value || status.value !== 'profilePending') return;
  stop();
  checking.value = true;
  try {
    await account.readProfile();
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
  <div class="card state-card">
    <span class="loading-dot" aria-hidden="true"></span>
    <h2>{{ title }}</h2>
    <p role="status">{{ text }}</p>
    <p v-if="attempts >= delays.length" class="field-help">
      Автоматическая проверка приостановлена. Можно проверить готовность вручную.
    </p>
    <button class="button secondary" :disabled="checking" @click="retry">
      {{ checking ? 'Проверяем…' : 'Проверить готовность' }}
    </button>
  </div>
</template>
