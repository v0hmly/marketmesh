<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import type { ThemePreference } from '../../../shared/api/types';
import { useSession, useTheme, type ConfirmedSettings } from '../../../shell/context';
import { GuardMismatchError } from '../../../shell/session';
import { accountError } from '../errors';

/**
 * Выбор темы в боковой панели кабинета. Выбор сохраняется сам, с CAS по версии настроек,
 * подтверждённой shell. Запись уходит после короткой паузы: стрелки на закрытом списке
 * меняют значение по шагу, и промежуточные темы не сохраняются. Конфликт и неизвестный
 * исход не повторяются вслепую: настройки перечитываются, и список показывает
 * действующую тему.
 */
const SAVE_DELAY = 500;
const session = useSession();
const theme = useTheme();
const choices: { value: ThemePreference; label: string }[] = [
  { value: 'system', label: 'Системная' },
  { value: 'light', label: 'Светлая' },
  { value: 'dark', label: 'Тёмная' },
];
const label = (value: ThemePreference) =>
  choices.find((choice) => choice.value === value)?.label ?? value;
const selected = ref<ThemePreference>('system');
const saving = ref(false);
const checking = ref(false);
/**
 * Исход записи неизвестен: новый выбор возможен только после сверки с сервером.
 * shallowRef: `before` сравнивается по ссылке с подтверждёнными настройками shell.
 */
const unverified = shallowRef<{
  wanted: ThemePreference;
  conflict: boolean;
  before: ConfirmedSettings | null;
} | null>(null);
const failure = ref('');
const feedback = ref('');
const retryButton = ref<HTMLButtonElement | null>(null);
let revision = 0;
let timer: ReturnType<typeof setTimeout> | undefined;

const current = computed(() => theme.confirmed.value);
const permitted = computed(() => session.state.value.status === 'authenticated');
const busy = computed(() => saving.value || checking.value);
// The select stays enabled while a write is in flight: disabling it would drop keyboard focus.
const disabled = computed(
  () => !permitted.value || !current.value || checking.value || unverified.value !== null,
);
const describedBy = computed(() =>
  failure.value ? 'theme-pick-error' : feedback.value ? 'theme-pick-result' : undefined,
);

function stopTimer() {
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
}
function reset() {
  revision++;
  stopTimer();
  saving.value = false;
  checking.value = false;
  unverified.value = null;
  failure.value = '';
  feedback.value = '';
  selected.value = current.value?.settings.theme ?? 'system';
}
async function recover() {
  try {
    await session.bootstrap();
  } catch {
    /* Shell presents the session state. */
  }
}
/** Сверяет ожидаемую тему с подтверждёнными настройками после записи с неясным исходом. */
function settle(after: ConfirmedSettings) {
  const pending = unverified.value;
  if (!pending) return;
  unverified.value = null;
  selected.value = after.settings.theme;
  if (after.settings.theme === pending.wanted) {
    failure.value = '';
    feedback.value = 'Тема сохранена и применится на всех ваших устройствах.';
    return;
  }
  feedback.value = '';
  failure.value = pending.conflict
    ? `Тему изменили в другом окне, поэтому ваш выбор не сохранён. Сейчас действует «${label(after.settings.theme)}» — выберите тему ещё раз, если нужна другая.`
    : `Выбор не сохранился. Сейчас действует «${label(after.settings.theme)}» — выберите тему ещё раз, если нужна другая.`;
}
async function verify() {
  const pending = unverified.value;
  if (!pending) return;
  const attempt = revision;
  checking.value = true;
  failure.value = '';
  try {
    await theme.load();
  } finally {
    if (attempt === revision) checking.value = false;
  }
  if (attempt !== revision || !unverified.value) return;
  const after = current.value;
  if (after && after !== pending.before) {
    settle(after);
    return;
  }
  failure.value =
    'Не удалось проверить, сохранилась ли тема. Проверьте ещё раз, прежде чем выбирать другую.';
  await nextTick();
  retryButton.value?.focus();
}
async function save() {
  timer = undefined;
  if (saving.value) {
    schedule();
    return;
  }
  const base = current.value;
  const value = selected.value;
  if (!base || !permitted.value || unverified.value || value === base.settings.theme) return;
  const attempt = revision;
  saving.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const settings = await session.updateSettings(
      { theme: value, expectedVersion: base.settings.version },
      base.guard,
    );
    if (attempt !== revision) return;
    theme.accept(settings, base.guard);
    const confirmed = current.value;
    if (confirmed?.settings !== settings) {
      // Shell kept a newer version (for example, read in the meantime): show what applies.
      unverified.value = { wanted: value, conflict: true, before: null };
      if (confirmed) settle(confirmed);
      return;
    }
    feedback.value = 'Тема сохранена и применится на всех ваших устройствах.';
  } catch (error) {
    if (attempt !== revision) return;
    if (error instanceof GuardMismatchError) {
      reset();
      await recover();
      return;
    }
    if (
      error instanceof ConnectError &&
      [Code.InvalidArgument, Code.PermissionDenied, Code.ResourceExhausted].includes(error.code)
    ) {
      // Сервер точно отклонил запись: тема осталась прежней.
      selected.value = base.settings.theme;
      failure.value = accountError(error);
      return;
    }
    unverified.value = {
      wanted: value,
      conflict: error instanceof ConnectError && error.code === Code.Aborted,
      before: base,
    };
    saving.value = false;
    stopTimer();
    if (error instanceof ConnectError && error.code === Code.Unauthenticated) await recover();
    if (attempt === revision) await verify();
  } finally {
    if (attempt === revision) {
      saving.value = false;
      // A newer choice made during the write goes out with the version just confirmed.
      if (!unverified.value && current.value && selected.value !== current.value.settings.theme)
        schedule();
    }
  }
}
function schedule() {
  stopTimer();
  timer = setTimeout(() => void save(), SAVE_DELAY);
}
function change(event: Event) {
  if (disabled.value) return;
  selected.value = (event.target as HTMLSelectElement).value as ThemePreference;
  failure.value = '';
  feedback.value = '';
  schedule();
}
function commitNow() {
  if (timer === undefined) return;
  stopTimer();
  void save();
}

watch(
  current,
  (value, previous) => {
    // A reread elsewhere (for example, the shell's retry) settles an unknown outcome too.
    if (unverified.value && value && value !== previous && !checking.value) {
      settle(value);
      return;
    }
    if (!saving.value && timer === undefined && !unverified.value)
      selected.value = value?.settings.theme ?? 'system';
  },
  { immediate: true },
);
watch(() => [session.state.value.generation, session.state.value.subjectId] as const, reset, {
  flush: 'sync',
});
onBeforeUnmount(() => {
  revision++;
  stopTimer();
});
</script>

<template>
  <div class="field theme-setting">
    <label for="theme-pick">Тема оформления</label>
    <span class="select-wrap">
      <select
        id="theme-pick"
        :value="selected"
        :disabled="disabled"
        :aria-busy="busy"
        :aria-describedby="describedBy"
        @change="change"
        @blur="commitNow"
      >
        <option v-for="choice in choices" :key="choice.value" :value="choice.value">
          {{ choice.label }}
        </option>
      </select>
    </span>
    <p v-if="failure" id="theme-pick-error" class="notice error" role="alert">{{ failure }}</p>
    <button
      v-if="unverified && failure && !busy"
      ref="retryButton"
      type="button"
      class="button text-button"
      @click="verify"
    >
      Проверить тему
    </button>
    <p v-if="feedback" id="theme-pick-result" class="notice success" role="status">
      {{ feedback }}
    </p>
  </div>
</template>
