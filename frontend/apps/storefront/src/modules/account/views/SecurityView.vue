<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import type { GetCredentialsResponse, SessionInfo } from '../../../shared/api/security';
import { Code } from '@connectrpc/connect';
import { authErrorReason } from '../../../shared/api/errors';
import { useSession } from '../../../shell/context';
import {
  validateEmail,
  validatePasswordStrength,
  validatePasswordRepeat,
  validateLoginCode,
  normalizeCodeInput,
} from '../../../shared/validation';
import { createSecurityApi, securityError } from '../security/api';
import AccountNav from '../components/AccountNav.vue';
import PasswordRules from '../components/PasswordRules.vue';

const session = useSession();
const api = createSecurityApi();
const credentials = shallowRef<GetCredentialsResponse | null>(null);
const sessions = shallowRef<SessionInfo[]>([]);
const busy = ref(false);
const failure = ref('');
const feedback = ref('');
const currentPassword = ref('');
const newPassword = ref('');
const repeatPassword = ref('');
const emailPassword = ref('');
const newEmail = ref('');
const codePassword = ref('');
const code = ref('');
const challenge = shallowRef<{ id: Uint8Array; enabled: boolean } | null>(null);
const attempted = ref('');
const authenticated = computed(() => session.state.value.status === 'authenticated');
const passwordError = computed(() =>
  attempted.value === 'password'
    ? (validatePasswordStrength(newPassword.value) ??
      validatePasswordRepeat(newPassword.value, repeatPassword.value))
    : null,
);
const emailError = computed(() =>
  attempted.value === 'email' ? validateEmail(newEmail.value) : null,
);
const codeError = computed(() =>
  attempted.value === 'code' ? validateLoginCode(code.value) : null,
);
const cooldown = computed(
  () =>
    credentials.value &&
    credentials.value.newDeviceCooldownUntilUnix > BigInt(Math.floor(Date.now() / 1000)),
);
const date = (unix: bigint) => new Date(Number(unix) * 1000).toLocaleString('ru-RU');
let active = true;
let revision = 0;
let lastFailure: unknown;
function erase() {
  revision++;
  credentials.value = null;
  sessions.value = [];
  currentPassword.value =
    newPassword.value =
    repeatPassword.value =
    emailPassword.value =
    newEmail.value =
    codePassword.value =
    code.value =
      '';
  challenge.value = null;
  attempted.value = '';
}
watch(
  () => [session.state.value.generation, session.state.value.status],
  () => {
    if (!authenticated.value) erase();
  },
  { flush: 'sync' },
);
watch(authenticated, (yes) => {
  if (yes && !busy.value) void read();
});
onBeforeUnmount(() => {
  active = false;
  erase();
});
async function read() {
  if (!authenticated.value || busy.value) return;
  busy.value = true;
  failure.value = '';
  let attempt = revision;
  try {
    // Only reads may recover and retry after the access cookie expires.
    await session.bootstrap();
    if (!active || !authenticated.value) return;
    attempt = revision;
    const owner = session.capture();
    const result = await session.withSession(owner, () =>
      Promise.all([api.getCredentials({}), api.listSessions({})]),
    );
    if (!active || attempt !== revision) return;
    credentials.value = result[0];
    sessions.value = result[1].sessions;
  } catch (error) {
    if (active && attempt === revision) failure.value = securityError(error);
  } finally {
    if (active) busy.value = false;
  }
}
async function run<T>(
  action: () => Promise<T>,
  success: string,
  endsSession = false,
): Promise<T | undefined> {
  if (!authenticated.value || busy.value) return;
  const owner = session.capture();
  busy.value = true;
  failure.value = feedback.value = '';
  lastFailure = undefined;
  try {
    const result = endsSession
      ? await session.endSession(action, owner)
      : await session.withSession(owner, action);
    if (active) feedback.value = success;
    return result;
  } catch (error) {
    lastFailure = error;
    if (active) failure.value = securityError(error);
  } finally {
    if (active) {
      busy.value = false;
      // A definitive rejection has restored this identity under a fresh generation.
      if (endsSession && authenticated.value && session.state.value.subjectId === owner.subjectId) {
        const message = failure.value;
        await read();
        if (active && session.state.value.subjectId === owner.subjectId) failure.value = message;
      }
    }
  }
}
async function changePassword() {
  attempted.value = 'password';
  if (passwordError.value || !currentPassword.value) return;
  const old = new TextEncoder().encode(currentPassword.value);
  const next = new TextEncoder().encode(newPassword.value);
  try {
    await run(
      () => api.changePassword({ currentPassword: old, newPassword: next }),
      'Пароль изменён. Все сеансы закрыты. Войдите с новым паролем.',
      true,
    );
  } finally {
    old.fill(0);
    next.fill(0);
    currentPassword.value = newPassword.value = repeatPassword.value = '';
  }
}
async function changeEmail() {
  attempted.value = 'email';
  if (emailError.value || !emailPassword.value) return;
  const password = new TextEncoder().encode(emailPassword.value);
  const email = newEmail.value.trim();
  try {
    await run(
      () => api.startEmailChange({ newEmail: email, password }),
      'Письмо подтверждения отправлено на новую почту, ссылка отмены — на прежнюю.',
    );
  } finally {
    password.fill(0);
    emailPassword.value = '';
  }
}
async function startCode() {
  if (!credentials.value || !codePassword.value) return;
  const password = new TextEncoder().encode(codePassword.value);
  const enabled = !credentials.value.loginCodeEnabled;
  const owner = session.capture();
  try {
    const result = await run(
      () => api.startLoginCodeChange({ enabled, password }),
      'Код отправлен на вашу почту. Он действует 10 минут.',
    );
    if (
      result &&
      active &&
      session.state.value.generation === owner.generation &&
      session.state.value.subjectId === owner.subjectId
    ) {
      if (result.challengeId.length !== 16 || result.codeExpiresInSeconds < 1n) {
        failure.value = 'Ответ не подтверждён. Запросите новый код.';
        return;
      }
      challenge.value = { id: result.challengeId, enabled };
    }
  } finally {
    password.fill(0);
    codePassword.value = '';
  }
}
async function confirmCode() {
  attempted.value = 'code';
  if (codeError.value || !challenge.value) return;
  const pending = challenge.value;
  const owner = session.capture();
  const value = code.value;
  try {
    await run(
      () =>
        api.completeLoginCodeChange({
          challengeId: pending.id,
          enabled: pending.enabled,
          code: value,
        }),
      'Настройка входа изменена. Все сеансы закрыты. Войдите снова.',
      true,
    );
  } finally {
    code.value = '';
    if (
      active &&
      authenticated.value &&
      session.state.value.subjectId === owner.subjectId &&
      credentials.value !== null &&
      credentials.value.loginCodeEnabled !== pending.enabled &&
      (authErrorReason(lastFailure, Code.InvalidArgument, 'CODE_MISMATCH') ||
        authErrorReason(lastFailure, Code.FailedPrecondition, 'CODE_REISSUED'))
    )
      challenge.value = pending;
  }
}
async function revoke(item: SessionInfo) {
  await run(
    () => api.revokeSession({ sessionId: item.sessionId }),
    item.current ? 'Текущий сеанс закрыт.' : 'Сеанс закрыт.',
    item.current,
  );
  if (!item.current) await read();
}
async function logoutAll() {
  await run(() => api.logoutAll({}), 'Все сеансы закрыты.', true);
}
void read();
</script>

<template>
  <section aria-labelledby="security-title">
    <div class="page-heading">
      <div>
        <p class="eyebrow">Личный кабинет</p>
        <h1 id="security-title">Защитите свой аккаунт.</h1>
        <p class="lede">Почта, пароль и подтверждение входа.</p>
      </div>
    </div>
    <AccountNav />
    <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
    <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
    <div v-if="!authenticated" class="card state-card">
      <h2>Войдите в аккаунт.</h2>
      <RouterLink class="button primary" to="/login"
        >Войти <span aria-hidden="true">↗</span></RouterLink
      >
    </div>
    <template v-else>
      <button class="button secondary" :disabled="busy" @click="read">
        {{ busy ? 'Проверяем…' : 'Обновить данные' }}
      </button>
      <div v-if="credentials" class="profile-layout">
        <section class="card profile-card">
          <h2>Почта для входа.</h2>
          <p>{{ credentials.email }}</p>
          <p class="field-help">
            {{ credentials.emailVerified ? 'Почта подтверждена.' : 'Почта ещё не подтверждена.' }}
          </p>
          <form novalidate :aria-busy="busy" @submit.prevent="changeEmail">
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Смена почты</legend>
              <div class="field">
                <label for="security-email">Новая почта</label
                ><input
                  id="security-email"
                  v-model="newEmail"
                  type="email"
                  autocomplete="email"
                  :aria-invalid="!!emailError"
                  :aria-describedby="emailError ? 'security-email-error' : undefined"
                /><span v-if="emailError" id="security-email-error" class="field-error">{{
                  emailError
                }}</span>
              </div>
              <div class="field">
                <label for="email-password">Текущий пароль для смены почты</label
                ><input
                  id="email-password"
                  v-model="emailPassword"
                  type="password"
                  autocomplete="current-password"
                  aria-describedby="email-help"
                /><span id="email-help" class="field-help"
                  >Подтвердите новый адрес по ссылке из письма. После этого потребуется войти
                  снова.</span
                >
              </div>
              <button class="button primary" :disabled="busy || !emailPassword">
                Отправить подтверждение
              </button>
            </fieldset>
          </form>
        </section>
        <section class="card profile-card">
          <h2>Пароль.</h2>
          <form novalidate :aria-busy="busy" @submit.prevent="changePassword">
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Смена пароля</legend>
              <div class="field">
                <label for="old-password">Текущий пароль</label
                ><input
                  id="old-password"
                  v-model="currentPassword"
                  type="password"
                  autocomplete="current-password"
                />
              </div>
              <div class="field">
                <label for="new-password">Новый пароль</label
                ><input
                  id="new-password"
                  v-model="newPassword"
                  type="password"
                  autocomplete="new-password"
                  :aria-invalid="!!passwordError"
                  :aria-describedby="
                    [
                      'password-help',
                      passwordError && 'password-error',
                      newPassword && 'password-rules',
                    ]
                      .filter(Boolean)
                      .join(' ')
                  "
                /><span id="password-help" class="field-help"
                  >От 8 до 64 символов: строчная и заглавная латинские буквы, цифра и
                  спецсимвол.</span
                >
              </div>
              <PasswordRules v-if="newPassword" id="password-rules" :password="newPassword" />
              <div class="field">
                <label for="repeat-password">Повторите новый пароль</label
                ><input
                  id="repeat-password"
                  v-model="repeatPassword"
                  type="password"
                  autocomplete="new-password"
                  :aria-invalid="!!passwordError"
                  :aria-describedby="passwordError ? 'password-error' : undefined"
                /><span v-if="passwordError" id="password-error" class="field-error">{{
                  passwordError
                }}</span>
              </div>
              <button class="button primary" :disabled="busy || !currentPassword">
                Изменить пароль
              </button>
            </fieldset>
          </form>
        </section>
        <section class="card profile-card">
          <h2>Код при входе.</h2>
          <p>
            {{
              credentials.loginCodeEnabled
                ? 'После пароля требуется код из письма.'
                : 'Вы входите с почтой и паролем.'
            }}
          </p>
          <form v-if="!challenge" novalidate :aria-busy="busy" @submit.prevent="startCode">
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Подтверждение входа</legend>
              <div class="field">
                <label for="code-password">Пароль для настройки входа</label
                ><input
                  id="code-password"
                  v-model="codePassword"
                  type="password"
                  autocomplete="current-password"
                  aria-describedby="code-policy-help"
                /><span id="code-policy-help" class="field-help"
                  >Изменение нужно подтвердить кодом из письма. Все сеансы будут закрыты.</span
                >
              </div>
              <button class="button primary" :disabled="busy || !codePassword">
                {{
                  credentials.loginCodeEnabled
                    ? 'Отключить подтверждение входа'
                    : 'Включить подтверждение входа'
                }}
              </button>
            </fieldset>
          </form>
          <form v-else novalidate :aria-busy="busy" @submit.prevent="confirmCode">
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Проверка кода</legend>
              <div class="field">
                <label for="security-code">Код из письма</label
                ><input
                  id="security-code"
                  :value="code"
                  inputmode="numeric"
                  autocomplete="one-time-code"
                  maxlength="6"
                  :aria-invalid="!!codeError"
                  :aria-describedby="
                    codeError ? 'security-code-help security-code-error' : 'security-code-help'
                  "
                  @input="code = normalizeCodeInput(($event.target as HTMLInputElement).value)"
                /><span id="security-code-help" class="field-help"
                  >Шесть цифр из последнего письма. После трёх ошибок придёт новый код.</span
                ><span v-if="codeError" id="security-code-error" class="field-error">{{
                  codeError
                }}</span>
              </div>
              <button class="button primary" :disabled="busy">Подтвердить настройку</button>
            </fieldset>
          </form>
        </section>
        <section class="card profile-card">
          <h2>Ваши сеансы.</h2>
          <p v-if="cooldown" class="field-help">
            После нового входа другие сеансы защищены до
            {{ date(credentials.newDeviceCooldownUntilUnix) }}. Текущий сеанс можно закрыть сейчас.
          </p>
          <ul>
            <li v-for="item in sessions" :key="Array.from(item.sessionId).join(',')">
              <p>
                {{ item.current ? 'Этот сеанс' : 'Другой сеанс' }} · {{ date(item.createdAtUnix) }}
              </p>
              <button
                class="button secondary"
                :disabled="busy || (!item.current && !!cooldown)"
                @click="revoke(item)"
              >
                Закрыть {{ item.current ? 'текущий' : 'этот' }} сеанс
              </button>
            </li>
          </ul>
          <button class="button text-button" :disabled="busy || !!cooldown" @click="logoutAll">
            Закрыть все сеансы
          </button>
        </section>
      </div>
    </template>
  </section>
</template>
