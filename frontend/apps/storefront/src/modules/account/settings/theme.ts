import type { AccountController } from '../api/controller';
import { readonly, ref, shallowRef, watch, type Ref, type ShallowRef } from 'vue';
import type { AccountSettings, ThemePreference } from '../api/types';
import type { SessionController, SessionGuard } from '../../../shell/session';

/** Последние подтверждённые сервером настройки и владелец, для которого они прочитаны. */
export interface ConfirmedSettings {
  readonly settings: AccountSettings;
  readonly guard: SessionGuard;
}

export interface ThemeController {
  readonly preference: Readonly<Ref<ThemePreference>>;
  readonly failed: Readonly<Ref<boolean>>;
  /** Версия для CAS при сохранении темы; null, пока настройки не подтверждены. */
  readonly confirmed: Readonly<ShallowRef<ConfirmedSettings | null>>;
  load(): Promise<void>;
  accept(settings: AccountSettings, guard: SessionGuard): void;
  dispose(): void;
}
/** Only confirmed preferences are applied. No account data goes to browser storage. */
export function createThemeController(
  session: SessionController,
  account: Pick<AccountController, 'readSettings'>,
  target: HTMLElement = document.documentElement,
): ThemeController {
  const preference = ref<ThemePreference>('system');
  const failed = ref(false);
  const confirmed = shallowRef<ConfirmedSettings | null>(null);
  let owner: SessionGuard | null = null;
  let version = 0n;
  let revision = 0;
  let disposed = false;
  let flight: { revision: number; promise: Promise<void> } | null = null;
  const system =
    typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-color-scheme: dark)')
      : null;
  function apply() {
    target.dataset.themePreference = preference.value;
    target.dataset.theme =
      preference.value === 'system' ? (system?.matches ? 'dark' : 'light') : preference.value;
  }
  function matches(guard: SessionGuard) {
    const state = session.state.value;
    return (
      !disposed &&
      state.generation === guard.generation &&
      state.subjectId === guard.subjectId &&
      Boolean(guard.subjectId) &&
      ['authenticated', 'checking'].includes(state.status)
    );
  }
  function reset() {
    revision++;
    owner = null;
    version = 0n;
    failed.value = false;
    confirmed.value = null;
    flight = null;
    preference.value = 'system';
    apply();
  }
  function accept(settings: AccountSettings, guard: SessionGuard) {
    if (
      !matches(guard) ||
      settings.version < version ||
      Array.from(settings.subjectId, (value) => value.toString(16).padStart(2, '0')).join('') !==
        guard.subjectId
    )
      return;
    owner = { ...guard };
    version = settings.version;
    preference.value = settings.theme;
    confirmed.value = { settings, guard: { ...guard } };
    failed.value = false;
    apply();
  }
  function load(): Promise<void> {
    if (disposed || session.state.value.status !== 'authenticated') return Promise.resolve();
    if (flight?.revision === revision) return flight.promise;
    const guard = session.capture();
    owner = { ...guard };
    const attempt = revision;
    const requestedVersion = version;
    const promise = account
      .readSettings(guard)
      .then((settings) => {
        if (attempt === revision) accept(settings, guard);
      })
      .catch(() => {
        if (attempt === revision && matches(guard) && version === requestedVersion)
          failed.value = true;
      })
      .finally(() => {
        if (flight?.revision === attempt) flight = null;
      });
    flight = { revision: attempt, promise };
    return promise;
  }
  apply();
  system?.addEventListener('change', apply);
  const stop = watch(
    session.state,
    (state) => {
      if (
        !state.subjectId ||
        (owner && (owner.subjectId !== state.subjectId || owner.generation !== state.generation)) ||
        ['anonymous', 'switching', 'signingOut', 'uncertain', 'unsupported'].includes(state.status)
      )
        reset();
      if (state.status === 'authenticated' && !owner) void load();
    },
    { immediate: true, flush: 'sync' },
  );
  return {
    preference: readonly(preference),
    failed: readonly(failed),
    confirmed,
    load,
    accept,
    dispose() {
      disposed = true;
      stop();
      system?.removeEventListener('change', apply);
      reset();
    },
  };
}
