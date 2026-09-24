import { useAccount } from '../api/controller';

import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { onBeforeRouteLeave, useRoute } from 'vue-router';

import { Code, ConnectError } from '@connectrpc/connect';

import { Gender, type Profile } from '../api/types';

import { isProfilePending } from '../api/errors';

import { useSession } from '../../../shell/context';

import { GuardMismatchError, type SessionGuard } from '../../../shell/session';

import { accountError } from '../errors';

import type { GetCredentialsResponse } from '../../auth/public';

import {
  ageOf,
  birthLabel,
  publicLineOf,
  validateIdentity,
  yearWord,
  type IdentityDraft,
  type IdentityErrors,
} from './validation';

import { avatarEnabled } from '../../../shared/features';
/** View-scoped state: CAS, owner changes, pending projection and private draft cleanup. */
export function useIdentityEditor() {
  const avatarURL = ref('');

  /** Почта для входа приходит из раздела безопасности (Auth), а не из профиля User. */
  const credentials = shallowRef<GetCredentialsResponse | null>(null);

  const route = useRoute();

  let jumpedToSecurity = false;

  /**
   * На экране открыта одна форма: правка личных данных или действие во входе и сеансах.
   * Смена пароля, кода и выход на всех устройствах закрывают сеанс и стёрли бы черновик
   * личных данных, поэтому они ждут, пока правку сохранят или отменят.
   */
  const securityActive = ref(false);

  const securityLock = 'Сначала сохраните или отмените изменения личных данных.';

  const session = useSession();

  const account = useAccount();

  const current = shallowRef<Profile | null>(null);

  const latest = shallowRef<Profile | null>(null);

  const guard = shallowRef<SessionGuard | null>(null);

  const editing = ref(false);

  const draft = ref<IdentityDraft>({
    displayName: '',
    lastName: '',
    birthDate: '',
    phone: '',
    city: '',
  });

  const gender = ref<Gender>(Gender.UNSPECIFIED);

  const loading = ref(false);

  const saving = ref(false);

  const failure = ref('');

  const feedback = ref('');

  const pending = ref(false);

  const reconcile = ref(false);

  const attempted = ref(false);

  const errors = ref<IdentityErrors>({});

  let revision = 0;

  let active = true;

  let pendingTimer: ReturnType<typeof setTimeout> | undefined;

  const pollAttempts = ref(0);

  const pollDelays = [1500, 3000, 6000, 12000] as const;

  const permitted = computed(() =>
    ['authenticated', 'profilePending'].includes(session.state.value.status),
  );

  const recheckingOwner = computed(() => {
    const state = session.state.value;
    return (
      state.status === 'checking' &&
      current.value !== null &&
      guard.value !== null &&
      state.subjectId === guard.value.subjectId &&
      state.generation === guard.value.generation
    );
  });

  const busy = computed(() => loading.value || saving.value || recheckingOwner.value);

  const shown = computed<IdentityDraft & { gender: Gender }>(() =>
    editing.value
      ? { ...draft.value, gender: gender.value }
      : {
          displayName: current.value?.displayName ?? '',
          lastName: current.value?.lastName ?? '',
          birthDate: current.value?.birthDate ?? '',
          phone: current.value?.phone ?? '',
          city: current.value?.city ?? '',
          gender: current.value?.gender ?? Gender.UNSPECIFIED,
        },
  );

  const shownAge = computed(() => ageOf(shown.value.birthDate));

  const ageText = computed(() =>
    shownAge.value === null ? null : `${shownAge.value} ${yearWord(shownAge.value)}`,
  );

  const showAge = computed(() => current.value?.showAge ?? false);

  const publicLine = computed(() => publicLineOf({ ...shown.value, showAge: showAge.value }));

  const initials = computed(() => {
    const source = `${shown.value.displayName.trim()} ${shown.value.lastName.trim()}`.trim();
    return (
      Array.from(source)
        .filter((char) => char !== ' ')
        .slice(0, 2)
        .join('')
        .toLocaleUpperCase('ru') || '—'
    );
  });

  const fullName = computed(
    () =>
      [shown.value.displayName.trim(), shown.value.lastName.trim()].filter(Boolean).join(' ') ||
      'Без имени',
  );

  const memberSince = computed(() => {
    const created = current.value?.createdAtUnix ?? 0n;
    if (created < 1n) return '';
    return new Date(Number(created) * 1000).toLocaleDateString('ru-RU', {
      day: 'numeric',
      month: 'long',
      year: 'numeric',
    });
  });

  const genderChoices: { value: Gender; label: string }[] = [
    { value: Gender.UNSPECIFIED, label: 'Не указывать' },
    { value: Gender.FEMALE, label: 'Женский' },
    { value: Gender.MALE, label: 'Мужской' },
  ];

  const genderLabel = (value: Gender) =>
    genderChoices.find((choice) => choice.value === value && value !== Gender.UNSPECIFIED)?.label ??
    'Не указан';

  const dirty = computed(
    () =>
      editing.value &&
      current.value !== null &&
      (draft.value.displayName !== current.value.displayName ||
        draft.value.lastName !== current.value.lastName ||
        draft.value.birthDate !== current.value.birthDate ||
        draft.value.phone !== current.value.phone ||
        draft.value.city !== current.value.city ||
        gender.value !== current.value.gender),
  );

  function stopPendingTimer() {
    if (pendingTimer !== undefined) clearTimeout(pendingTimer);
    pendingTimer = undefined;
  }

  function schedulePendingPoll() {
    stopPendingTimer();
    if (
      !active ||
      !permitted.value ||
      !(pending.value || session.state.value.status === 'profilePending') ||
      loading.value ||
      pollAttempts.value >= pollDelays.length
    )
      return;
    pendingTimer = setTimeout(() => {
      pendingTimer = undefined;
      pollAttempts.value++;
      void readProfile();
    }, pollDelays[pollAttempts.value]);
  }

  function clearPrivateState() {
    revision++;
    stopPendingTimer();
    pollAttempts.value = 0;
    current.value = null;
    latest.value = null;
    guard.value = null;
    editing.value = false;
    draft.value = { displayName: '', lastName: '', birthDate: '', phone: '', city: '' };
    gender.value = Gender.UNSPECIFIED;
    errors.value = {};
    attempted.value = false;
    failure.value = '';
    feedback.value = '';
    reconcile.value = false;
    pending.value = false;
    loading.value = false;
    saving.value = false;
  }

  function acceptProfile(profile: Profile) {
    current.value = profile;
    guard.value = session.capture();
    latest.value = null;
    reconcile.value = false;
    editing.value = false;
    errors.value = {};
    attempted.value = false;
  }

  async function readProfile(compare = false) {
    if (busy.value || !permitted.value) return;
    const attempt = revision;
    loading.value = true;
    failure.value = '';
    feedback.value = '';
    try {
      const profile = await account.readProfile(guard.value ?? undefined);
      if (attempt !== revision) return;
      pending.value = false;
      if (compare && current.value) {
        latest.value = profile;
        reconcile.value = true;
        feedback.value = 'Актуальные данные загружены. Ваш черновик сохранён ниже.';
      } else acceptProfile(profile);
    } catch (error) {
      if (attempt !== revision) return;
      if (error instanceof GuardMismatchError && session.state.value.status !== 'profilePending') {
        clearPrivateState();
        await recoverSession();
        return;
      }
      pending.value = isProfilePending(error) || session.state.value.status === 'profilePending';
      if (!pending.value) failure.value = accountError(error);
    } finally {
      if (attempt === revision) {
        loading.value = false;
        schedulePendingPoll();
      }
    }
  }

  function startEditing() {
    if (!current.value || busy.value || reconcile.value || securityActive.value) return;
    draft.value = {
      displayName: current.value.displayName,
      lastName: current.value.lastName,
      birthDate: current.value.birthDate,
      phone: current.value.phone,
      city: current.value.city,
    };
    gender.value = current.value.gender;
    editing.value = true;
    attempted.value = false;
    errors.value = {};
    failure.value = '';
    feedback.value = '';
    void nextTick(() => document.getElementById('id-first')?.focus());
  }

  async function cancelEditing() {
    if (busy.value) return;
    if (dirty.value && !window.confirm('Удалить несохранённые изменения личных данных?')) return;
    editing.value = false;
    attempted.value = false;
    errors.value = {};
    await nextTick();
    document.getElementById('edit-identity')?.focus();
  }

  function profileInput(extra: Partial<Parameters<typeof account.updateProfile>[0]> = {}) {
    const base = editing.value ? draft.value : shown.value;
    return {
      displayName: base.displayName,
      bio: current.value!.bio,
      lastName: base.lastName,
      birthDate: base.birthDate,
      gender: editing.value ? gender.value : (current.value!.gender as Gender),
      phone: base.phone,
      city: base.city,
      showAge: current.value!.showAge,
      expectedVersion: current.value!.version,
      ...extra,
    };
  }

  async function mutate(input: ReturnType<typeof profileInput>) {
    if (!current.value || !guard.value || busy.value || !permitted.value) return;
    const attempt = revision;
    saving.value = true;
    failure.value = '';
    feedback.value = '';
    try {
      const profile = await account.updateProfile(input, guard.value);
      if (attempt !== revision) return;
      acceptProfile(profile);
    } catch (error) {
      if (attempt !== revision) return;
      if (error instanceof GuardMismatchError) {
        clearPrivateState();
        await recoverSession();
        return;
      }
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        failure.value =
          'Данные изменились в другом окне. Ваш черновик сохранён. Перечитайте актуальные данные и выберите, что сохранить.';
        reconcile.value = true;
      } else if (
        error instanceof ConnectError &&
        [Code.InvalidArgument, Code.PermissionDenied, Code.ResourceExhausted].includes(error.code)
      ) {
        failure.value = accountError(error);
      } else {
        failure.value =
          'Сохранение не подтверждено. Ваш черновик сохранён. Перечитайте данные, прежде чем отправлять изменения снова.';
        reconcile.value = true;
        if (error instanceof ConnectError && error.code === Code.Unauthenticated)
          await recoverSession();
      }
    } finally {
      if (attempt === revision) saving.value = false;
    }
  }

  async function save() {
    if (!editing.value || reconcile.value) return;
    attempted.value = true;
    errors.value = validateIdentity(draft.value);
    if (Object.keys(errors.value).length) {
      await nextTick();
      document.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
      return;
    }
    await mutate(profileInput());
    if (!failure.value) feedback.value = 'Данные сохранены.';
  }

  async function toggleAge(event: Event) {
    const next = (event.target as HTMLInputElement).checked;
    if (!current.value || editing.value || reconcile.value || busy.value) return;
    await mutate(profileInput({ showAge: next }));
    if (!failure.value)
      feedback.value = next
        ? 'Возраст теперь виден в ваших отзывах.'
        : 'Возраст скрыт. В отзывах останутся имя и город.';
  }

  function acceptLatest() {
    if (!latest.value) return;
    acceptProfile(latest.value);
    failure.value = '';
    feedback.value = 'Показана актуальная версия данных.';
  }

  function keepDraft() {
    if (!latest.value || !editing.value) return;
    current.value = latest.value;
    guard.value = session.capture();
    latest.value = null;
    reconcile.value = false;
    failure.value = '';
    feedback.value =
      'Черновик подготовлен к сохранению поверх прочитанной версии. Проверьте поля и нажмите «Сохранить».';
  }

  async function recoverSession() {
    try {
      await session.bootstrap();
    } catch {
      /* Shell presents the session state. */
    }
  }

  async function retryPending() {
    pollAttempts.value = 0;
    stopPendingTimer();
    await readProfile();
  }

  watch(() => session.state.value.generation, clearPrivateState, { flush: 'sync' });

  watch(
    () => [session.state.value.status, session.state.value.subjectId] as const,
    ([status, subjectId]) => {
      if (status === 'authenticated' && guard.value && subjectId !== guard.value.subjectId)
        clearPrivateState();
      if (['anonymous', 'switching', 'signingOut', 'uncertain', 'unsupported'].includes(status))
        clearPrivateState();
    },
    { flush: 'sync' },
  );

  watch(
    () => [session.state.value.generation, session.state.value.subjectId, permitted.value] as const,
    () => {
      if (session.state.value.status === 'authenticated' && !current.value) void readProfile();
    },
    { immediate: true },
  );

  watch(
    () => session.state.value.status,
    (status) => {
      if (status === 'profilePending') schedulePendingPoll();
      else stopPendingTimer();
    },
    { immediate: true },
  );

  watch(
    draft,
    () => {
      if (attempted.value) errors.value = validateIdentity(draft.value);
    },
    { deep: true },
  );

  // /account/security ведёт сюда с #security: раздел ниже личных данных, поэтому переходим к нему,
  // когда карточки над ним уже отрисованы, и переводим туда фокус.
  watch(current, async (value) => {
    if (!value || jumpedToSecurity || route.hash !== '#security') return;
    jumpedToSecurity = true;
    await nextTick();
    const target = document.getElementById('security');
    if (!target) return;
    if (typeof target.scrollIntoView === 'function') target.scrollIntoView({ block: 'start' });
    target.focus({ preventScroll: true });
  });

  function beforeUnload(event: BeforeUnloadEvent) {
    if (dirty.value) {
      event.preventDefault();
      event.returnValue = '';
    }
  }

  window.addEventListener('beforeunload', beforeUnload);

  onBeforeRouteLeave(
    () =>
      !dirty.value ||
      window.confirm('Есть несохранённые изменения. Покинуть страницу и удалить черновик?'),
  );

  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', beforeUnload);
    active = false;
    clearPrivateState();
  });
  return {
    avatarURL,
    credentials,
    securityActive,
    securityLock,
    session,
    account,
    current,
    latest,
    editing,
    draft,
    gender,
    loading,
    saving,
    failure,
    feedback,
    pending,
    reconcile,
    errors,
    active,
    pollAttempts,
    pollDelays,
    permitted,
    recheckingOwner,
    busy,
    shown,
    ageText,
    showAge,
    publicLine,
    initials,
    fullName,
    memberSince,
    genderChoices,
    genderLabel,
    dirty,
    readProfile,
    startEditing,
    cancelEditing,
    save,
    toggleAge,
    acceptLatest,
    keepDraft,
    retryPending,
    Gender,
    birthLabel,
    avatarEnabled,
  };
}
