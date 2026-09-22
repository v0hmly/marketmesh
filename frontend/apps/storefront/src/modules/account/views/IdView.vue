<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { onBeforeRouteLeave } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import { Gender, type Profile } from '../../../shared/api/types';
import { isProfilePending } from '../../../shared/api/errors';
import { useSession } from '../../../shell/context';
import { GuardMismatchError, type SessionGuard } from '../../../shell/session';
import { accountError } from '../errors';
import {
  ageOf,
  birthLabel,
  validateIdentity,
  yearWord,
  type IdentityDraft,
  type IdentityErrors,
} from '../validation';
import AccountNav from '../components/AccountNav.vue';
import AvatarEditor from '../components/AvatarEditor.vue';
import { avatarEnabled } from '../../../shared/features';
const avatarURL = ref('');

const session = useSession();
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
const publicLine = computed(() => {
  const parts = [shown.value.displayName.trim(), shown.value.city.trim()];
  if (showAge.value && ageText.value !== null) parts.push(ageText.value);
  return parts.filter(Boolean).join(', ') || 'Покупатель MarketMesh';
});
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
    const profile = await session.readProfile(guard.value ?? undefined);
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
  if (!current.value || busy.value || reconcile.value) return;
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
function profileInput(extra: Partial<Parameters<typeof session.updateProfile>[0]> = {}) {
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
    const profile = await session.updateProfile(input, guard.value);
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
</script>

<template>
  <section aria-labelledby="id-title">
    <AccountNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ЛИЧНЫЙ КАБИНЕТ</span>
        <h1 id="id-title">MarketMesh ID</h1>
        <p class="lede">
          Один аккаунт для покупок, магазина и поддержки. Здесь ваши личные и публичные данные.
        </p>
      </div>
      <span class="section-number" aria-hidden="true">07 / MARKETMESH ID</span>
    </div>
    <div v-if="pending || session.state.value.status === 'profilePending'" class="card state-card">
      <span class="loading-dot" aria-hidden="true"></span>
      <h2>Готовим ваш профиль</h2>
      <p role="status">
        Вход выполнен. Нам нужно немного времени, чтобы подготовить личное пространство.
      </p>
      <p v-if="pollAttempts >= pollDelays.length" class="field-help">
        Автоматическая проверка приостановлена. Можно проверить готовность вручную.
      </p>
      <button class="button secondary" :disabled="loading" @click="retryPending">
        {{ loading ? 'Проверяем…' : 'Проверить готовность' }}
      </button>
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
    </div>
    <div v-else-if="!permitted && !recheckingOwner" class="card state-card">
      <template v-if="['unknown', 'checking'].includes(session.state.value.status)"
        ><span class="loading-dot" aria-hidden="true"></span>
        <h2>Открываем ваш кабинет</h2>
        <p role="status">Проверяем сессию…</p></template
      >
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите в аккаунт, чтобы посмотреть и изменить свои данные.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!current" class="card state-card" :aria-busy="loading">
      <p v-if="loading" role="status">Загружаем данные…</p>
      <template v-else
        ><h2>Данные пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="readProfile()">
          Повторить загрузку
        </button></template
      >
    </div>
    <div v-else v-show="!recheckingOwner" class="id-content" :inert="recheckingOwner">
      <div class="card identity-card id-hero" aria-label="Ваш MarketMesh ID">
        <div class="initials" aria-hidden="true">
          <img v-if="avatarURL" :src="avatarURL" alt="" /><template v-else>{{ initials }}</template>
        </div>
        <div class="id-hero-text">
          <h2>{{ fullName }}</h2>
          <p>MarketMesh ID</p>
          <p v-if="memberSince">Вы с нами с {{ memberSince }}</p>
        </div>
        <div class="privacy-note">
          <span aria-hidden="true">↳</span>
          <p>Личные данные видите только вы. Публичными делитесь сами.</p>
        </div>
      </div>
      <AvatarEditor v-if="avatarEnabled" :initials="initials" @image="avatarURL = $event" />
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div v-if="reconcile" class="reconcile-panel" aria-labelledby="id-reconcile-title">
        <h2 id="id-reconcile-title">Сверим с сохранёнными данными</h2>
        <button class="button secondary" :disabled="busy" @click="readProfile(true)">
          {{ loading ? 'Читаем…' : 'Перечитать актуальные данные' }}
        </button>
        <div v-if="latest" class="latest-profile">
          <h4>Сейчас на сервере</h4>
          <dl>
            <dt>Имя</dt>
            <dd>{{ latest.displayName || 'Не указано' }}</dd>
            <dt>Фамилия</dt>
            <dd>{{ latest.lastName || 'Не указана' }}</dd>
            <dt>Город</dt>
            <dd>{{ latest.city || 'Не указан' }}</dd>
            <dt>Телефон</dt>
            <dd>{{ latest.phone || 'Не указан' }}</dd>
          </dl>
          <div class="button-row">
            <button class="button secondary" :disabled="busy" @click="acceptLatest">
              Принять актуальные данные</button
            ><button v-if="editing" class="button text-button" :disabled="busy" @click="keepDraft">
              Оставить мой черновик для сохранения
            </button>
          </div>
        </div>
      </div>
      <section class="card profile-card" aria-labelledby="personal-title">
        <div class="card-heading">
          <div>
            <h2 id="personal-title">Личные данные</h2>
            <p class="subtle">Нужны для заказов и поддержки. Другим покупателям они не видны.</p>
          </div>
          <button
            v-if="!editing"
            id="edit-identity"
            class="button secondary"
            :disabled="busy || reconcile"
            @click="startEditing"
          >
            Изменить данные
          </button>
          <span v-else-if="dirty" class="draft-badge">Есть изменения</span>
        </div>
        <dl v-if="!editing" class="id-rows">
          <div class="id-row">
            <dt>ИМЯ</dt>
            <dd :class="{ subtle: !current.displayName }">
              {{ current.displayName || 'Не указано' }}
            </dd>
          </div>
          <div class="id-row">
            <dt>ФАМИЛИЯ</dt>
            <dd :class="{ subtle: !current.lastName }">{{ current.lastName || 'Не указана' }}</dd>
          </div>
          <div class="id-row">
            <dt>ДАТА РОЖДЕНИЯ</dt>
            <dd :class="{ subtle: !current.birthDate }">{{ birthLabel(current.birthDate) }}</dd>
          </div>
          <div class="id-row">
            <dt>ПОЛ</dt>
            <dd :class="{ subtle: current.gender === Gender.UNSPECIFIED }">
              {{ genderLabel(current.gender) }}
            </dd>
          </div>
          <div class="id-row">
            <dt>ТЕЛЕФОН</dt>
            <dd :class="{ subtle: !current.phone }">{{ current.phone || 'Не указан' }}</dd>
          </div>
          <div class="id-row">
            <dt>ГОРОД</dt>
            <dd :class="{ subtle: !current.city }">{{ current.city || 'Не указан' }}</dd>
          </div>
        </dl>
        <form v-else novalidate :aria-busy="saving" @submit.prevent="save">
          <div class="id-form-grid">
            <div class="field">
              <label for="id-first">Имя<span aria-hidden="true"> *</span></label>
              <input
                id="id-first"
                v-model="draft.displayName"
                type="text"
                autocomplete="given-name"
                :disabled="busy"
                :aria-invalid="Boolean(errors.displayName)"
                :aria-describedby="
                  errors.displayName ? 'id-first-help id-first-error' : 'id-first-help'
                "
              />
              <span id="id-first-help" class="field-help"
                >Имя видно в ваших отзывах и нужно мастеру.</span
              >
              <span v-if="errors.displayName" id="id-first-error" class="field-error">{{
                errors.displayName
              }}</span>
            </div>
            <div class="field">
              <label for="id-last">Фамилия</label>
              <input
                id="id-last"
                v-model="draft.lastName"
                type="text"
                autocomplete="family-name"
                :disabled="busy"
                :aria-invalid="Boolean(errors.lastName)"
                :aria-describedby="errors.lastName ? 'id-last-help id-last-error' : 'id-last-help'"
              />
              <span id="id-last-help" class="field-help"
                >Необязательно. В отзывах не показывается.</span
              >
              <span v-if="errors.lastName" id="id-last-error" class="field-error">{{
                errors.lastName
              }}</span>
            </div>
            <div class="field">
              <label for="id-city">Город проживания</label>
              <input
                id="id-city"
                v-model="draft.city"
                type="text"
                autocomplete="address-level2"
                :disabled="busy"
                :aria-invalid="Boolean(errors.city)"
                :aria-describedby="errors.city ? 'id-city-help id-city-error' : 'id-city-help'"
              />
              <span id="id-city-help" class="field-help"
                >Подставим в доставку и покажем в отзывах.</span
              >
              <span v-if="errors.city" id="id-city-error" class="field-error">{{
                errors.city
              }}</span>
            </div>
            <div class="field">
              <label for="id-birth">Дата рождения</label>
              <input
                id="id-birth"
                v-model="draft.birthDate"
                type="date"
                :disabled="busy"
                :aria-invalid="Boolean(errors.birthDate)"
                :aria-describedby="
                  errors.birthDate ? 'id-birth-help id-birth-error' : 'id-birth-help'
                "
              />
              <span id="id-birth-help" class="field-help"
                >Нужна для изделий с возрастным ограничением.</span
              >
              <span v-if="errors.birthDate" id="id-birth-error" class="field-error">{{
                errors.birthDate
              }}</span>
            </div>
            <div class="field">
              <label for="id-phone">Телефон</label>
              <input
                id="id-phone"
                v-model="draft.phone"
                type="tel"
                autocomplete="tel"
                :disabled="busy"
                :aria-invalid="Boolean(errors.phone)"
                :aria-describedby="errors.phone ? 'id-phone-help id-phone-error' : 'id-phone-help'"
              />
              <span id="id-phone-help" class="field-help">7–15 цифр, можно с пробелами и +.</span>
              <span v-if="errors.phone" id="id-phone-error" class="field-error">{{
                errors.phone
              }}</span>
            </div>
            <fieldset class="field theme-options" :aria-busy="saving">
              <legend>Пол</legend>
              <label v-for="choice in genderChoices" :key="choice.value" class="theme-choice"
                ><input
                  v-model="gender"
                  type="radio"
                  name="gender"
                  :value="choice.value"
                  :disabled="busy"
                  :aria-label="choice.label"
                  aria-describedby="id-gender-help"
                /><span>{{ choice.label }}</span></label
              >
              <span id="id-gender-help" class="field-help"
                >Необязательно. Влияет только на подборки.</span
              >
            </fieldset>
          </div>
          <div class="form-footer">
            <span class="field-help">{{
              dirty ? 'Есть несохранённые изменения' : 'Проверьте данные перед сохранением'
            }}</span>
            <div class="button-row">
              <button
                class="button secondary"
                type="button"
                :disabled="busy"
                @click="cancelEditing"
              >
                Отменить</button
              ><button class="button primary" type="submit" :disabled="busy || !dirty">
                {{ saving ? 'Сохраняем…' : 'Сохранить' }} <span aria-hidden="true">↗</span>
              </button>
            </div>
          </div>
        </form>
      </section>
      <section class="card profile-card" aria-labelledby="public-title">
        <div class="card-heading">
          <div>
            <h2 id="public-title">Публичные данные</h2>
            <p class="subtle">
              Это видят другие покупатели рядом с вашими отзывами. Показывается только то, что
              заполнено в личных данных.
            </p>
          </div>
        </div>
        <div class="public-layout">
          <dl class="id-rows public-rows">
            <div class="id-row">
              <dt>ИМЯ</dt>
              <dd :class="{ subtle: !shown.displayName.trim() }">
                {{ shown.displayName.trim() || 'Не заполнено — не показывается' }}
              </dd>
            </div>
            <div class="id-row">
              <dt>ГОРОД</dt>
              <dd :class="{ subtle: !shown.city.trim() }">
                {{ shown.city.trim() || 'Не заполнен — не показывается' }}
              </dd>
            </div>
            <div class="id-row">
              <dt>ВОЗРАСТ</dt>
              <dd :class="{ subtle: !(showAge && ageText !== null) }">
                {{ ageText === null ? 'Дата рождения не указана' : showAge ? ageText : 'Скрыт' }}
              </dd>
            </div>
          </dl>
          <div class="public-preview">
            <span class="eyebrow">Так вас увидят в отзыве</span>
            <div class="review-preview">
              <div class="preview-heading">
                <span class="preview-avatar" aria-hidden="true">{{ initials.slice(0, 1) }}</span>
                <span class="line-meta"
                  ><span class="preview-name">{{ publicLine }}</span
                  ><span class="review-date">2 СЕНТЯБРЯ</span></span
                >
              </div>
              <p class="subtle preview-quote">
                «Глазурь ровная, ручка удобно ложится в ладонь. Пришла в плотной коробке с бумагой,
                ни одного скола.»
              </p>
            </div>
            <p class="subtle preview-note">
              Фамилия, телефон и пол в отзывах не показываются никогда.
            </p>
          </div>
        </div>
        <label class="theme-choice age-choice">
          <input
            type="checkbox"
            :checked="showAge"
            :disabled="busy || editing || reconcile || ageText === null"
            aria-describedby="age-switch-help"
            @change="toggleAge"
          />
          <span
            >Показывать возраст в отзывах
            <small id="age-switch-help">{{
              ageText === null
                ? 'Пока дата рождения не указана, показывать нечего.'
                : `Сейчас другие покупатели видят: ${showAge ? ageText : 'без возраста'}.`
            }}</small></span
          >
        </label>
      </section>
    </div>
    <p v-if="recheckingOwner" role="status">Проверяем сессию…</p>
  </section>
</template>
