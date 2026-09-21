<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import { useSession } from '../../../shell/context';
import { authErrorReason } from '../../../shared/api/errors';
import type { LoginChallenge } from '../../../shared/api/types';
import { analytics } from '../../../shared/analytics';
import {
  formatCodeTtl,
  normalizeCodeInput,
  passwordChecks,
  validateEmail,
  validateLoginCode,
  validatePasswordRepeat,
  validatePasswordStrength,
} from '../validation';

const props = defineProps<{ mode: 'login' | 'register' }>();
const session = useSession();
const router = useRouter();

const email = ref('');
const password = ref('');
const confirmPassword = ref('');
const attempted = ref(false);
const passwordStarted = ref(false);

const submitting = ref(false);
const succeeded = ref(false);
const failedAttempts = ref(0);
const serverError = ref<'' | 'wrongCredentials' | 'serverError' | 'locked'>('');

const step = ref<'credentials' | 'code'>('credentials');
const challenge = ref<LoginChallenge | null>(null);
const code = ref('');
const codeAttempted = ref(false);
const codeSubmitting = ref(false);
const codeSucceeded = ref(false);
const codeFailedAttempts = ref(0);
const codeError = ref<'' | 'wrongCode' | 'tooMany' | 'expired' | 'serverError'>('');
const resending = ref(false);
const codeResent = ref(false);

const registered = ref(false);
const verificationSent = ref(false);
const resendingVerification = ref(false);
const verificationResent = ref(false);
const verificationFailed = ref(false);

let requestSequence = 0;
let active = true;

const isRegister = computed(() => props.mode === 'register');
const sessionBusy = computed(() =>
  ['unknown', 'checking', 'switching', 'signingOut', 'unsupported'].includes(
    session.state.value.status,
  ),
);

const emailError = computed(() => (attempted.value ? validateEmail(email.value) : null));
const passwordError = computed(() => {
  if (!attempted.value) return null;
  if (isRegister.value) return validatePasswordStrength(password.value);
  return password.value ? null : 'Введите пароль, чтобы продолжить.';
});
const confirmError = computed(() =>
  attempted.value ? validatePasswordRepeat(password.value, confirmPassword.value) : null,
);
const requirements = computed(() => passwordChecks(password.value));

const locked = computed(() => serverError.value === 'locked');
const fieldsDisabled = computed(
  () => submitting.value || succeeded.value || locked.value || sessionBusy.value,
);
const noticeText = computed(() => {
  switch (serverError.value) {
    case 'wrongCredentials':
      return 'Почта или пароль указаны неверно. Проверьте данные и попробуйте снова.';
    case 'locked':
      return 'Слишком много неудачных попыток. Вход временно заблокирован — попробуйте снова через 15 минут.';
    case 'serverError':
      return isRegister.value
        ? 'Не удалось создать аккаунт. Проверьте соединение и попробуйте ещё раз.'
        : 'Не удалось подключиться к серверу. Проверьте соединение и попробуйте ещё раз.';
    default:
      return '';
  }
});
const submitLabel = computed(() => {
  if (submitting.value) return isRegister.value ? 'Создаём аккаунт…' : 'Входим…';
  if (locked.value) return 'Вход заблокирован';
  return isRegister.value ? 'Зарегистрироваться' : 'Войти';
});

const codeClientError = computed(() =>
  codeAttempted.value ? validateLoginCode(code.value) : null,
);
const codeFieldsDisabled = computed(
  () => codeSubmitting.value || codeSucceeded.value || resending.value || sessionBusy.value,
);
const codeNoticeText = computed(() => {
  switch (codeError.value) {
    case 'wrongCode':
      return 'Код неверный. Проверьте письмо и введите код ещё раз.';
    case 'tooMany':
      return 'Слишком много попыток с этим кодом. Запросите новый код.';
    case 'expired':
      return 'Срок действия кода истёк. Запросите новый код.';
    case 'serverError':
      return 'Не удалось проверить код. Проверьте соединение и попробуйте ещё раз.';
    default:
      return '';
  }
});
const codeSubmitLabel = computed(() =>
  codeSubmitting.value ? 'Проверяем код…' : 'Подтвердить вход',
);
const resendLabel = computed(() => (resending.value ? 'Отправляем код…' : 'Отправить код ещё раз'));

const emailDescribedBy = computed(() => {
  const ids: string[] = [];
  if (isRegister.value && !emailError.value) ids.push('reg-email-help');
  if (emailError.value) ids.push(isRegister.value ? 'reg-email-error' : 'login-email-error');
  return ids.join(' ') || undefined;
});
const passwordDescribedBy = computed(() => {
  const ids: string[] = [];
  if (isRegister.value && passwordStarted.value) ids.push('reg-password-requirements');
  if (passwordError.value)
    ids.push(isRegister.value ? 'reg-password-error' : 'login-password-error');
  return ids.join(' ') || undefined;
});
const codeDescribedBy = computed(() => {
  const ids = ['login-code-help'];
  if (codeClientError.value) ids.push('login-code-error');
  return ids.join(' ');
});

function onCodeInput(event: Event) {
  code.value = normalizeCodeInput((event.target as HTMLInputElement).value);
  codeError.value = '';
  codeResent.value = false;
}

function resetCodeStep() {
  step.value = 'credentials';
  challenge.value = null;
  code.value = '';
  codeAttempted.value = false;
  codeSubmitting.value = false;
  codeFailedAttempts.value = 0;
  codeError.value = '';
  codeResent.value = false;
  resending.value = false;
}

watch(
  () => props.mode,
  () => {
    requestSequence++;
    attempted.value = false;
    password.value = '';
    confirmPassword.value = '';
    serverError.value = '';
    registered.value = false;
    resetCodeStep();
  },
);
watch(
  () => session.state.value.generation,
  () => {
    // Own in-flight login also rotates the generation (intent); only a change
    // without a local submission means another tab switched or ended the session.
    if (submitting.value || codeSubmitting.value) return;
    password.value = '';
    confirmPassword.value = '';
    email.value = '';
    resetCodeStep();
  },
  { flush: 'sync' },
);
onBeforeUnmount(() => {
  active = false;
  requestSequence++;
  password.value = '';
  confirmPassword.value = '';
});

async function submitLogin() {
  if (fieldsDisabled.value) return;
  attempted.value = true;
  serverError.value = '';
  if (emailError.value || passwordError.value) return;
  submitting.value = true;
  const sequence = ++requestSequence;
  const address = email.value.trim();
  const bytes = new TextEncoder().encode(password.value);
  try {
    let pending: LoginChallenge;
    try {
      pending = await session.startLogin(address, bytes);
    } catch (error) {
      // Backend до выкатки MM-81 не знает кодового шага: входим прежним RPC.
      if (!(error instanceof ConnectError && error.code === Code.Unimplemented)) throw error;
      await session.login(address, bytes);
      if (!active || sequence !== requestSequence) return;
      succeeded.value = true;
      email.value = '';
      analytics.event('login_succeeded');
      await router.push('/account');
      return;
    }
    if (!active || sequence !== requestSequence) return;
    challenge.value = pending;
    step.value = 'code';
    password.value = '';
  } catch (error) {
    if (!active || sequence !== requestSequence) return;
    if (authErrorReason(error, Code.FailedPrecondition, 'LOGIN_LOCKED')) {
      serverError.value = 'locked';
    } else if (error instanceof ConnectError && error.code === Code.Unauthenticated) {
      failedAttempts.value += 1;
      serverError.value = failedAttempts.value >= 5 ? 'locked' : 'wrongCredentials';
    } else {
      serverError.value = 'serverError';
    }
  } finally {
    bytes.fill(0);
    submitting.value = false;
  }
}

async function submitCode() {
  if (codeFieldsDisabled.value || !challenge.value) return;
  codeAttempted.value = true;
  codeError.value = '';
  codeResent.value = false;
  if (codeClientError.value) return;
  codeSubmitting.value = true;
  const sequence = ++requestSequence;
  try {
    await session.completeLogin(challenge.value, code.value);
    if (!active || sequence !== requestSequence) return;
    codeSucceeded.value = true;
    email.value = '';
    analytics.event('login_succeeded');
    await router.push('/account');
  } catch (error) {
    if (!active || sequence !== requestSequence) return;
    if (authErrorReason(error, Code.InvalidArgument, 'CODE_MISMATCH')) {
      codeFailedAttempts.value += 1;
      if (codeFailedAttempts.value >= 3) {
        codeError.value = 'tooMany';
        code.value = '';
        codeAttempted.value = false;
      } else {
        codeError.value = 'wrongCode';
      }
    } else if (authErrorReason(error, Code.InvalidArgument, 'CODE_REISSUED')) {
      codeError.value = 'tooMany';
      code.value = '';
      codeAttempted.value = false;
    } else if (authErrorReason(error, Code.FailedPrecondition, 'CODE_EXPIRED')) {
      codeError.value = 'expired';
    } else {
      codeError.value = 'serverError';
    }
  } finally {
    codeSubmitting.value = false;
  }
}

async function resendCode() {
  if (!challenge.value || resending.value || codeSubmitting.value || codeSucceeded.value) return;
  resending.value = true;
  codeError.value = '';
  codeResent.value = false;
  try {
    challenge.value = await session.resendLoginCode(challenge.value);
    codeResent.value = true;
    code.value = '';
    codeAttempted.value = false;
    codeFailedAttempts.value = 0;
  } catch {
    codeError.value = 'serverError';
  } finally {
    resending.value = false;
  }
}

async function submitRegister() {
  if (fieldsDisabled.value) return;
  attempted.value = true;
  serverError.value = '';
  if (emailError.value || passwordError.value || confirmError.value) return;
  submitting.value = true;
  const sequence = ++requestSequence;
  const address = email.value.trim();
  const bytes = new TextEncoder().encode(password.value);
  try {
    await session.register(address, bytes);
    let sent = false;
    try {
      await session.requestEmailVerification(address);
      sent = true;
    } catch {
      // Backend до выкатки MM-81 не отправляет писем: показываем нейтральный исход.
      sent = false;
    }
    if (!active || sequence !== requestSequence) return;
    analytics.event('registration_request_completed');
    verificationSent.value = sent;
    registered.value = true;
    password.value = '';
    confirmPassword.value = '';
  } catch {
    if (active && sequence === requestSequence) serverError.value = 'serverError';
  } finally {
    bytes.fill(0);
    submitting.value = false;
  }
}

async function resendVerification() {
  if (resendingVerification.value) return;
  resendingVerification.value = true;
  verificationFailed.value = false;
  try {
    await session.requestEmailVerification(email.value.trim());
    verificationResent.value = true;
  } catch {
    verificationFailed.value = true;
  } finally {
    resendingVerification.value = false;
  }
}

function submit() {
  if (isRegister.value) void submitRegister();
  else void submitLogin();
}
</script>

<template>
  <section class="auth-layout" aria-labelledby="auth-title">
    <template v-if="registered">
      <div class="card state-card">
        <h1 id="auth-title" class="state-title">Аккаунт создан.</h1>
        <template v-if="verificationSent">
          <p>
            Мы отправили письмо для подтверждения на {{ email.trim() }}. Перейдите по ссылке в
            письме, чтобы войти.
          </p>
          <p v-if="verificationResent" class="notice success" role="status">
            Письмо отправлено повторно.
          </p>
          <p v-if="verificationFailed" class="notice error" role="alert">
            Не удалось отправить письмо. Проверьте соединение и попробуйте ещё раз.
          </p>
          <button
            v-if="!verificationResent"
            type="button"
            class="button secondary"
            :disabled="resendingVerification"
            @click="resendVerification"
          >
            {{ resendingVerification ? 'Отправляем письмо…' : 'Отправить письмо ещё раз' }}
          </button>
        </template>
        <template v-else>
          <p>Запрос обработан. Теперь войдите с вашей почтой и паролем.</p>
          <RouterLink class="button primary" to="/login"
            >Войти<span aria-hidden="true">↗</span></RouterLink
          >
        </template>
      </div>
    </template>

    <template v-else>
      <div class="auth-heading">
        <h1 id="auth-title">
          {{
            isRegister
              ? 'Начнём знакомство.'
              : step === 'code'
                ? 'Подтвердите вход.'
                : 'Рады вас видеть.'
          }}
        </h1>
        <p class="lede">
          {{
            isRegister
              ? 'Заведём аккаунт, чтобы возвращаться сюда было проще.'
              : step === 'code'
                ? `У вас включено подтверждение входа. Мы отправили код из шести цифр на ${email.trim()}.`
                : 'Войдите, чтобы продолжить с того места, где остановились.'
          }}
        </p>
      </div>

      <form
        v-if="step === 'credentials' || isRegister"
        class="card auth-card"
        novalidate
        :aria-busy="submitting || sessionBusy"
        @submit.prevent="submit"
      >
        <p v-if="noticeText" class="notice error" role="alert">{{ noticeText }}</p>
        <p v-if="succeeded" role="status" class="auth-progress">
          <span class="loading-dot" aria-hidden="true"></span>Вход выполнен. Переходим в личный
          кабинет…
        </p>
        <p v-if="sessionBusy && !submitting" role="status" class="subtle">
          Проверяем сессию перед вводом данных…
        </p>
        <template v-if="!succeeded">
          <div class="field">
            <label :for="isRegister ? 'reg-email' : 'login-email'">Почта</label>
            <input
              :id="isRegister ? 'reg-email' : 'login-email'"
              v-model="email"
              type="email"
              name="email"
              autocomplete="username"
              autocapitalize="none"
              spellcheck="false"
              :disabled="fieldsDisabled"
              :aria-invalid="Boolean(emailError)"
              :aria-describedby="emailDescribedBy"
            />
            <span v-if="isRegister && !emailError" id="reg-email-help" class="field-help"
              >Используется для входа. Проверьте перед отправкой.</span
            >
            <span
              v-if="emailError"
              :id="isRegister ? 'reg-email-error' : 'login-email-error'"
              class="field-error"
              >{{ emailError }}</span
            >
          </div>
          <div class="field">
            <label :for="isRegister ? 'reg-password' : 'login-password'">Пароль</label>
            <input
              :id="isRegister ? 'reg-password' : 'login-password'"
              v-model="password"
              type="password"
              name="password"
              :autocomplete="isRegister ? 'new-password' : 'current-password'"
              :disabled="fieldsDisabled"
              :aria-invalid="Boolean(passwordError)"
              :aria-describedby="passwordDescribedBy"
              @input="passwordStarted = true"
            />
            <ul
              v-if="isRegister && passwordStarted"
              id="reg-password-requirements"
              class="password-requirements"
            >
              <li
                v-for="req in requirements"
                :key="req.id"
                :class="req.ok ? 'requirement-met' : 'requirement-pending'"
              >
                <span aria-hidden="true">{{ req.ok ? '✓' : '·' }}</span>
                <span
                  >{{ req.label
                  }}<span class="visually-hidden">
                    — {{ req.ok ? 'выполнено' : 'ещё не выполнено' }}</span
                  ></span
                >
              </li>
            </ul>
            <span
              v-if="passwordError"
              :id="isRegister ? 'reg-password-error' : 'login-password-error'"
              class="field-error"
              >{{ passwordError }}</span
            >
          </div>
          <div v-if="isRegister" class="field">
            <label for="reg-confirm">Повторите пароль</label>
            <input
              id="reg-confirm"
              v-model="confirmPassword"
              type="password"
              name="confirmPassword"
              autocomplete="new-password"
              :disabled="fieldsDisabled"
              :aria-invalid="Boolean(confirmError)"
              :aria-describedby="confirmError ? 'reg-confirm-error' : undefined"
            />
            <span v-if="confirmError" id="reg-confirm-error" class="field-error">{{
              confirmError
            }}</span>
          </div>
          <button class="button primary wide" type="submit" :disabled="fieldsDisabled">
            <span>{{ submitLabel }}</span>
            <span v-if="!submitting && !locked" aria-hidden="true">↗</span>
          </button>
        </template>
      </form>

      <form
        v-else
        class="card auth-card"
        novalidate
        :aria-busy="codeSubmitting || sessionBusy"
        @submit.prevent="submitCode"
      >
        <p v-if="codeNoticeText" class="notice error" role="alert">{{ codeNoticeText }}</p>
        <p v-if="codeResent && !codeSucceeded" class="notice success" role="status">
          Новый код отправлен на почту. Прежний код больше не подойдёт.
        </p>
        <p v-if="codeSucceeded" role="status" class="auth-progress">
          <span class="loading-dot" aria-hidden="true"></span>Вход подтверждён. Переходим в личный
          кабинет…
        </p>
        <template v-if="!codeSucceeded">
          <div class="field">
            <label for="login-code">Код из письма</label>
            <input
              id="login-code"
              :value="code"
              type="text"
              name="code"
              inputmode="numeric"
              autocomplete="one-time-code"
              maxlength="6"
              :disabled="codeFieldsDisabled"
              :aria-invalid="Boolean(codeClientError)"
              :aria-describedby="codeDescribedBy"
              @input="onCodeInput"
            />
            <span id="login-code-help" class="field-help"
              >Шесть цифр. Код действует
              {{ challenge ? formatCodeTtl(challenge.codeExpiresInSeconds) : 'недолго' }}.</span
            >
            <span v-if="codeClientError" id="login-code-error" class="field-error">{{
              codeClientError
            }}</span>
          </div>
          <button class="button primary wide" type="submit" :disabled="codeFieldsDisabled">
            <span>{{ codeSubmitLabel }}</span>
            <span v-if="!codeSubmitting" aria-hidden="true">↗</span>
          </button>
          <div class="auth-code-actions">
            <button
              type="button"
              class="button secondary"
              :disabled="codeFieldsDisabled"
              @click="resendCode"
            >
              {{ resendLabel }}
            </button>
            <button
              type="button"
              class="button text-button"
              :disabled="codeFieldsDisabled"
              @click="resetCodeStep"
            >
              Изменить почту
            </button>
          </div>
        </template>
      </form>

      <p v-if="step === 'credentials'" class="auth-alternative">
        {{ isRegister ? 'Уже есть аккаунт?' : 'Нет аккаунта?' }}
        <RouterLink :to="isRegister ? '/login' : '/register'">{{
          isRegister ? 'Войти' : 'Зарегистрироваться'
        }}</RouterLink>
      </p>
    </template>
  </section>
</template>
