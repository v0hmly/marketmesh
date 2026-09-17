<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { onBeforeRouteLeave } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { AccountSettings, ThemePreference } from '../../../shared/api/types';
import { isProfilePending } from '../../../shared/api/errors';
import { useSession, useTheme } from '../../../shell/context';
import { GuardMismatchError, type SessionGuard } from '../../../shell/session';
import { accountError } from '../errors';
import AccountNav from '../components/AccountNav.vue';

const session = useSession();
const theme = useTheme();
const current = shallowRef<AccountSettings | null>(null);
const latest = shallowRef<AccountSettings | null>(null);
const guard = shallowRef<SessionGuard | null>(null);
const selected = ref<ThemePreference>('system');
const loading = ref(false);
const saving = ref(false);
const failure = ref('');
const feedback = ref('');
const pending = ref(false);
const reconcile = ref(false);
const attempts = ref(0);
const pollDelays = [1500, 3000, 6000, 12000];
const choices: { value: ThemePreference; label: string; description: string }[] = [
  {
    value: 'system',
    label: 'Как в системе',
    description: 'Следовать светлой или тёмной теме устройства.',
  },
  { value: 'light', label: 'Светлая', description: 'Светлый фон и тёмный текст.' },
  { value: 'dark', label: 'Тёмная', description: 'Тёмный фон и светлый текст.' },
];
const label = (value: ThemePreference) => choices.find((choice) => choice.value === value)?.label;
let revision = 0;
let active = true;
let timer: ReturnType<typeof setTimeout> | undefined;
const permitted = computed(() => session.state.value.status === 'authenticated');
const busy = computed(() => loading.value || saving.value);
const dirty = computed(() => current.value !== null && selected.value !== current.value.theme);
function stopTimer() {
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
}
function clear() {
  revision++;
  stopTimer();
  current.value = null;
  latest.value = null;
  guard.value = null;
  selected.value = 'system';
  failure.value = '';
  feedback.value = '';
  pending.value = false;
  reconcile.value = false;
  attempts.value = 0;
  loading.value = false;
  saving.value = false;
}
function schedule() {
  stopTimer();
  if (
    !active ||
    !(pending.value || session.state.value.status === 'profilePending') ||
    attempts.value >= pollDelays.length
  )
    return;
  timer = setTimeout(() => {
    timer = undefined;
    attempts.value++;
    void read();
  }, pollDelays[attempts.value]);
}
function accept(value: AccountSettings, owner: SessionGuard) {
  current.value = value;
  selected.value = value.theme;
  guard.value = owner;
  latest.value = null;
  reconcile.value = false;
  theme.accept(value, owner);
}
async function recover() {
  try {
    await session.bootstrap();
  } catch {
    /* Shell owns recovery. */
  }
}
async function read(compare = false) {
  if (
    !active ||
    busy.value ||
    (!permitted.value && session.state.value.status !== 'profilePending')
  )
    return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    if (session.state.value.status === 'profilePending') await session.readProfile();
    if (attempt !== revision || !permitted.value) return;
    const owner = guard.value ?? session.capture();
    const settings = await session.readSettings(owner);
    if (attempt !== revision) return;
    pending.value = false;
    stopTimer();
    theme.accept(settings, owner);
    if (compare && current.value) {
      latest.value = settings;
      reconcile.value = true;
      feedback.value = 'Сохранённая тема прочитана. Ваш выбор остался в форме.';
    } else accept(settings, owner);
  } catch (error) {
    if (attempt !== revision) return;
    if (error instanceof GuardMismatchError) {
      clear();
      await recover();
      return;
    }
    pending.value = isProfilePending(error) || session.state.value.status === 'profilePending';
    if (!pending.value) failure.value = accountError(error);
  } finally {
    if (attempt === revision) {
      loading.value = false;
      schedule();
    }
  }
}
async function save() {
  if (
    !current.value ||
    !guard.value ||
    !permitted.value ||
    busy.value ||
    reconcile.value ||
    !dirty.value
  )
    return;
  const attempt = revision;
  saving.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const settings = await session.updateSettings(
      { theme: selected.value, expectedVersion: current.value.version },
      guard.value,
    );
    if (attempt !== revision) return;
    accept(settings, guard.value);
    feedback.value = 'Оформление сохранено.';
  } catch (error) {
    if (attempt !== revision) return;
    if (error instanceof GuardMismatchError) {
      clear();
      await recover();
      return;
    }
    if (
      error instanceof ConnectError &&
      [Code.InvalidArgument, Code.PermissionDenied, Code.ResourceExhausted].includes(error.code)
    )
      failure.value = accountError(error);
    else {
      reconcile.value = true;
      failure.value =
        error instanceof ConnectError && error.code === Code.Aborted
          ? 'Настройки изменены в другом окне. Ваш выбор сохранён. Перечитайте актуальные данные.'
          : 'Сохранение не подтверждено. Перечитайте настройки перед новой отправкой. Ваш выбор сохранён.';
      if (error instanceof ConnectError && error.code === Code.Unauthenticated) await recover();
    }
  } finally {
    if (attempt === revision) saving.value = false;
  }
}
function acceptLatest() {
  if (!latest.value || !guard.value || busy.value || !permitted.value) return;
  accept(latest.value, guard.value);
  failure.value = '';
  feedback.value = 'Приняты актуальные настройки.';
}
function keepDraft() {
  if (!latest.value || busy.value || !permitted.value) return;
  current.value = latest.value;
  latest.value = null;
  guard.value = session.capture();
  reconcile.value = false;
  failure.value = '';
  feedback.value =
    'Ваш выбор подготовлен к сохранению. Подтвердите его кнопкой «Сохранить оформление».';
}
async function retry() {
  attempts.value = 0;
  stopTimer();
  await read(reconcile.value);
}
watch(() => session.state.value.generation, clear, { flush: 'sync' });
watch(
  () => [session.state.value.status, session.state.value.subjectId] as const,
  ([status, subject]) => {
    if (
      (guard.value && subject && subject !== guard.value.subjectId) ||
      ['anonymous', 'switching', 'signingOut', 'uncertain', 'unsupported'].includes(status)
    )
      clear();
  },
  { flush: 'sync' },
);
watch(
  () => [session.state.value.generation, session.state.value.subjectId, permitted.value] as const,
  () => {
    if (permitted.value && !current.value) void read();
  },
  { immediate: true },
);
watch(
  () => session.state.value.status,
  (status) => {
    if (status === 'profilePending') schedule();
    else if (!pending.value) stopTimer();
  },
  { immediate: true },
);
function beforeUnload(event: BeforeUnloadEvent) {
  if (dirty.value) {
    event.preventDefault();
    event.returnValue = '';
  }
}
window.addEventListener('beforeunload', beforeUnload);
onBeforeRouteLeave(
  () => !dirty.value || window.confirm('Есть несохранённый выбор оформления. Покинуть страницу?'),
);
onBeforeUnmount(() => {
  active = false;
  window.removeEventListener('beforeunload', beforeUnload);
  clear();
});
</script>
<template>
  <section aria-labelledby="settings-title">
    <AccountNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ЛИЧНЫЙ КАБИНЕТ</span>
        <h1 id="settings-title">Оформление</h1>
        <p class="lede">Выберите тему, в которой вам удобно.</p>
      </div>
      <span class="section-number" aria-hidden="true">03 / НАСТРОЙКИ</span>
    </div>
    <div v-if="pending || session.state.value.status === 'profilePending'" class="card state-card">
      <h2>Готовим ваши настройки</h2>
      <p role="status">Профиль создаётся. Настройки появятся после подготовки аккаунта.</p>
      <p v-if="attempts >= pollDelays.length">Автоматическая проверка приостановлена.</p>
      <button class="button secondary" :disabled="busy" @click="retry">Проверить готовность</button>
    </div>
    <div v-else-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы сохранить оформление аккаунта.</p>
        <RouterLink class="button primary" to="/login">Перейти ко входу</RouterLink></template
      >
    </div>
    <div v-else-if="!current" class="card state-card" :aria-busy="loading">
      <p v-if="loading" role="status">Загружаем настройки…</p>
      <template v-else
        ><h2>Настройки пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read()">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="card profile-card settings-card">
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div v-if="reconcile" class="reconcile-panel" aria-labelledby="settings-reconcile-title">
        <h2 id="settings-reconcile-title">Сверим настройки</h2>
        <button class="button secondary" :disabled="busy" @click="read(true)">
          Перечитать актуальные данные</button
        ><template v-if="latest"
          ><p class="settings-latest">Сейчас на сервере: {{ label(latest.theme) }}.</p>
          <div class="button-row">
            <button class="button secondary" :disabled="busy" @click="acceptLatest">
              Принять актуальные настройки</button
            ><button class="button text-button" :disabled="busy" @click="keepDraft">
              Оставить мой выбор для сохранения
            </button>
          </div></template
        >
      </div>
      <form :aria-busy="saving" @submit.prevent="save">
        <fieldset class="theme-options" :disabled="busy">
          <legend>Тема оформления</legend>
          <p id="theme-help" class="subtle">
            Тема изменится после сохранения. Выбор действует для вашего аккаунта на разных
            устройствах.
          </p>
          <label v-for="choice in choices" :key="choice.value" class="theme-choice"
            ><input
              v-model="selected"
              type="radio"
              :aria-label="choice.label"
              name="theme"
              :value="choice.value"
              :aria-describedby="`theme-help theme-${choice.value}-help`"
            /><span
              >{{ choice.label
              }}<small :id="`theme-${choice.value}-help`">{{ choice.description }}</small></span
            ></label
          >
        </fieldset>
        <div class="form-footer">
          <span class="field-help">{{ dirty ? 'Есть несохранённый выбор' : 'Всё сохранено' }}</span
          ><button class="button primary" type="submit" :disabled="busy || !dirty || reconcile">
            {{ saving ? 'Сохраняем…' : 'Сохранить оформление' }}
          </button>
        </div>
      </form>
    </div>
  </section>
</template>
