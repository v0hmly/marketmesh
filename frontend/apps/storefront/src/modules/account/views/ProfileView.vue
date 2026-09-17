<script setup lang="ts">
import AccountNav from '../components/AccountNav.vue';
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { onBeforeRouteLeave } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Profile } from '../../../shared/api/types';
import { isProfilePending } from '../../../shared/api/errors';
import { useSession } from '../../../shell/context';
import { GuardMismatchError, type SessionGuard } from '../../../shell/session';
import { accountError } from '../errors';
import { trimDisplayName, validateProfile } from '../validation';

const session = useSession();
const current = shallowRef<Profile | null>(null);
const latest = shallowRef<Profile | null>(null);
const guard = shallowRef<SessionGuard | null>(null);
const displayName = ref('');
const bio = ref('');
const loading = ref(false);
const saving = ref(false);
const failure = ref('');
const feedback = ref('');
const pending = ref(false);
const reconcile = ref(false);
const errors = ref<{ displayName?: string; bio?: string }>({});
let revision = 0;
let active = true;
let pendingTimer: ReturnType<typeof setTimeout> | undefined;
const pollAttempts = ref(0);
const pollDelays = [1500, 3000, 6000, 12000] as const;
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
const permitted = computed(() =>
  ['authenticated', 'profilePending'].includes(session.state.value.status),
);
const dirty = computed(
  () =>
    current.value !== null &&
    (displayName.value !== current.value.displayName || bio.value !== current.value.bio),
);
const initials = computed(() =>
  Array.from(current.value?.displayName || 'Вы')
    .slice(0, 2)
    .join('')
    .toLocaleUpperCase('ru'),
);
const nameCount = computed(() => Array.from(trimDisplayName(displayName.value)).length);
const bioCount = computed(() => Array.from(bio.value).length);

function clearPrivateState() {
  revision++;
  stopPendingTimer();
  pollAttempts.value = 0;
  current.value = null;
  latest.value = null;
  guard.value = null;
  displayName.value = '';
  bio.value = '';
  errors.value = {};
  failure.value = '';
  feedback.value = '';
  reconcile.value = false;
  pending.value = false;
  loading.value = false;
  saving.value = false;
}

function acceptProfile(profile: Profile) {
  const owner = session.capture();
  current.value = profile;
  displayName.value = profile.displayName;
  bio.value = profile.bio;
  guard.value = owner;
  latest.value = null;
  reconcile.value = false;
  errors.value = {};
}

async function readProfile(compare = false) {
  if (loading.value || saving.value || !permitted.value) return;
  const requestRevision = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const profile = await session.readProfile(guard.value ?? undefined);
    if (requestRevision !== revision) return;
    pending.value = false;
    if (compare && current.value) {
      latest.value = profile;
      reconcile.value = true;
      feedback.value = 'Актуальные данные загружены. Ваш черновик сохранён ниже.';
    } else acceptProfile(profile);
  } catch (error) {
    if (requestRevision !== revision) return;
    if (error instanceof GuardMismatchError && session.state.value.status !== 'profilePending') {
      clearPrivateState();
      await recoverSession();
      return;
    }
    pending.value = isProfilePending(error) || session.state.value.status === 'profilePending';
    if (!pending.value) failure.value = accountError(error);
  } finally {
    if (requestRevision === revision) {
      loading.value = false;
      schedulePendingPoll();
    }
  }
}

async function save() {
  if (
    !current.value ||
    !guard.value ||
    saving.value ||
    loading.value ||
    reconcile.value ||
    !permitted.value
  )
    return;
  errors.value = validateProfile(displayName.value, bio.value);
  failure.value = '';
  feedback.value = '';
  if (Object.keys(errors.value).length) return;
  const requestRevision = revision;
  saving.value = true;
  try {
    const profile = await session.updateProfile(
      { displayName: displayName.value, bio: bio.value, expectedVersion: current.value.version },
      guard.value,
    );
    if (requestRevision !== revision) return;
    acceptProfile(profile);
    feedback.value = 'Изменения сохранены.';
  } catch (error) {
    if (requestRevision !== revision) return;
    if (error instanceof GuardMismatchError) {
      clearPrivateState();
      await recoverSession();
      return;
    }
    if (error instanceof ConnectError && error.code === Code.Aborted) {
      failure.value =
        'Профиль изменился в другом окне. Ваш черновик сохранён. Перечитайте актуальные данные и выберите, что сохранить.';
      reconcile.value = true;
    } else if (
      error instanceof ConnectError &&
      [Code.InvalidArgument, Code.PermissionDenied, Code.ResourceExhausted].includes(error.code)
    ) {
      failure.value = accountError(error);
    } else {
      failure.value =
        'Сохранение не подтверждено. Ваш черновик сохранён. Перечитайте профиль, прежде чем отправлять изменения снова.';
      reconcile.value = true;
      if (error instanceof ConnectError && error.code === Code.Unauthenticated)
        await recoverSession();
    }
  } finally {
    if (requestRevision === revision) saving.value = false;
  }
}

function acceptLatest() {
  if (!latest.value) return;
  acceptProfile(latest.value);
  failure.value = '';
  feedback.value = 'Показана актуальная версия профиля.';
}

function keepDraft() {
  if (!latest.value) return;
  current.value = latest.value;
  guard.value = session.capture();
  latest.value = null;
  reconcile.value = false;
  failure.value = '';
  feedback.value =
    'Черновик подготовлен к сохранению поверх прочитанной версии. Проверьте поля и нажмите «Сохранить изменения».';
}

watch(() => session.state.value.generation, clearPrivateState, { flush: 'sync' });
watch(
  () => [session.state.value.status, session.state.value.subjectId] as const,
  ([status, subjectId]) => {
    if (status === 'authenticated' && guard.value && subjectId !== guard.value.subjectId)
      clearPrivateState();
  },
  { flush: 'sync' },
);
watch(
  () => session.state.value.status,
  (status) => {
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
watch(
  () => session.state.value.status,
  (status) => {
    if (status === 'profilePending') schedulePendingPoll();
    else stopPendingTimer();
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
onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', beforeUnload);
  active = false;
  clearPrivateState();
});
onBeforeRouteLeave(
  () =>
    !dirty.value ||
    window.confirm('Есть несохранённые изменения. Покинуть страницу и удалить черновик?'),
);
</script>

<template>
  <section aria-labelledby="profile-title">
    <AccountNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ЛИЧНЫЙ КАБИНЕТ</span>
        <h1 id="profile-title">О себе</h1>
        <p class="lede">Небольшие детали, из которых складывается знакомство.</p>
      </div>
      <span class="section-number" aria-hidden="true">01 / ПРОФИЛЬ</span>
    </div>
    <div v-if="!permitted" class="card state-card">
      <template v-if="['unknown', 'checking'].includes(session.state.value.status)"
        ><span class="loading-dot" aria-hidden="true"></span>
        <h2>Открываем ваш кабинет</h2>
        <p role="status">Проверяем сессию…</p></template
      >
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите в аккаунт, чтобы посмотреть и изменить свой профиль.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div
      v-else-if="pending || session.state.value.status === 'profilePending'"
      class="card state-card"
    >
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
    <div v-else-if="!current" class="card state-card" :aria-busy="loading">
      <p v-if="loading" role="status">Загружаем профиль…</p>
      <template v-else
        ><h2>Профиль пока недоступен</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="readProfile()">
          Повторить загрузку
        </button></template
      >
    </div>
    <div v-else class="profile-layout">
      <aside class="card identity-card" aria-label="Ваш профиль">
        <div class="initials" aria-hidden="true">{{ initials }}</div>
        <h2>{{ current.displayName || 'Ваше имя' }}</h2>
        <p>
          {{
            current.displayName
              ? 'Здесь можно быть собой.'
              : 'Как к вам обращаться? Добавьте имя в профиле.'
          }}
        </p>
        <div class="privacy-note">
          <span aria-hidden="true">↳</span>
          <p>Данные этого раздела доступны только вам.</p>
        </div>
      </aside>
      <div class="card profile-card">
        <div class="card-heading">
          <div>
            <p class="eyebrow">ЛИЧНЫЕ ДАННЫЕ</p>
            <h2>Расскажите о себе</h2>
          </div>
          <span v-if="dirty" class="draft-badge">Есть изменения</span>
        </div>
        <p class="subtle">Оба поля необязательны. Вы можете изменить их в любой момент.</p>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
        <div v-if="reconcile" class="reconcile-panel" aria-labelledby="reconcile-title">
          <h3 id="reconcile-title">Сверим с сохранённым профилем</h3>
          <button class="button secondary" :disabled="loading || saving" @click="readProfile(true)">
            {{ loading ? 'Читаем…' : 'Перечитать актуальные данные' }}
          </button>
          <div v-if="latest" class="latest-profile">
            <h4>Сейчас на сервере</h4>
            <dl>
              <dt>Имя</dt>
              <dd>{{ latest.displayName || 'Не указано' }}</dd>
              <dt>О себе</dt>
              <dd class="plain-text">{{ latest.bio || 'Не заполнено' }}</dd>
            </dl>
            <div class="button-row">
              <button class="button secondary" @click="acceptLatest">
                Принять актуальный профиль</button
              ><button class="button text-button" @click="keepDraft">
                Оставить мой черновик для сохранения
              </button>
            </div>
          </div>
        </div>
        <form novalidate :aria-busy="saving" @submit.prevent="save">
          <div class="field">
            <div class="label-line">
              <label for="display-name">Имя</label
              ><span id="name-count" class="field-count">{{ nameCount }} / 80</span>
            </div>
            <input
              id="display-name"
              v-model="displayName"
              name="name"
              autocomplete="nickname"
              :disabled="saving || loading"
              :aria-invalid="Boolean(errors.displayName)"
              :aria-describedby="
                errors.displayName ? 'name-error name-help name-count' : 'name-help name-count'
              "
            />
            <span id="name-help" class="field-help"
              >Имя, которое удобно вам. Фамилия необязательна.</span
            >
            <span v-if="errors.displayName" id="name-error" class="field-error">{{
              errors.displayName
            }}</span>
          </div>
          <div class="field">
            <div class="label-line">
              <label for="bio">О себе</label
              ><span id="bio-count" class="field-count">{{ bioCount }} / 1000</span>
            </div>
            <textarea
              id="bio"
              v-model="bio"
              name="bio"
              rows="6"
              :disabled="saving || loading"
              :aria-invalid="Boolean(errors.bio)"
              :aria-describedby="errors.bio ? 'bio-error bio-help bio-count' : 'bio-help bio-count'"
            ></textarea>
            <span id="bio-help" class="field-help"
              >Пара слов о вас, ваших интересах или любимых вещах.</span
            >
            <span v-if="errors.bio" id="bio-error" class="field-error">{{ errors.bio }}</span>
          </div>
          <div class="form-footer">
            <span class="field-help">{{
              dirty ? 'Изменения ещё не сохранены' : 'Всё сохранено'
            }}</span
            ><button
              class="button primary"
              type="submit"
              :disabled="!dirty || saving || loading || reconcile"
            >
              {{ saving ? 'Сохраняем…' : 'Сохранить изменения' }} <span aria-hidden="true">↗</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  </section>
</template>
