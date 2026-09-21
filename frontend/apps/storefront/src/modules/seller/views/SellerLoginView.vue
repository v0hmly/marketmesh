<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import { useSellerApi, useSession } from '../../../shell/context';
import { authErrorReason, sellerErrorReason } from '../../../shared/api/errors';
import type { Shop } from '../../../shared/api/seller';
import type { LoginChallenge } from '../../../shared/api/types';
import {
  formatCodeTtl,
  normalizeCodeInput,
  validateEmail,
  validateLoginCode,
} from '../../../shared/validation';

const session = useSession();
const sellerApi = useSellerApi();
const router = useRouter();

const email = ref('');
const password = ref('');
const attempted = ref(false);

const submitting = ref(false);
const failedAttempts = ref(0);
const serverError = ref<'' | 'wrongCredentials' | 'serverError' | 'locked'>('');

const step = ref<'credentials' | 'code'>('credentials');
const challenge = ref<LoginChallenge | null>(null);
const code = ref('');
const codeAttempted = ref(false);
const codeSubmitting = ref(false);
const codeFailedAttempts = ref(0);
const codeError = ref<'' | 'wrongCode' | 'tooMany' | 'expired' | 'serverError'>('');
const resending = ref(false);
const codeResent = ref(false);

type PortalState = '' | 'loading' | 'pending' | 'rejected' | 'blocked' | 'noShop' | 'shopError';
const portalState = ref<PortalState>('');
const shop = ref<Shop | null>(null);
/** Backend без кодового шага StartLogin: портал продавца требует код всегда, fallback на покупательский вход невозможен. */
const portalUnavailable = ref(false);

let requestSequence = 0;
let active = true;

const sessionBusy = computed(() =>
  ['unknown', 'checking', 'switching', 'signingOut', 'unsupported'].includes(
    session.state.value.status,
  ),
);

const emailError = computed(() => (attempted.value ? validateEmail(email.value) : null));
const passwordError = computed(() => {
  if (!attempted.value) return null;
  return password.value ? null : 'Введите пароль, чтобы продолжить.';
});

const locked = computed(() => serverError.value === 'locked');
const fieldsDisabled = computed(
  () => submitting.value || portalState.value !== '' || locked.value || sessionBusy.value,
);
const noticeText = computed(() => {
  switch (serverError.value) {
    case 'wrongCredentials':
      return 'Почта или пароль указаны неверно. Проверьте данные и попробуйте снова.';
    case 'locked':
      return 'Слишком много неудачных попыток. Вход в портал заблокирован — попробуйте снова через 15 минут.';
    case 'serverError':
      return 'Не удалось подключиться к серверу. Проверьте соединение и попробуйте ещё раз.';
    default:
      return '';
  }
});
const submitLabel = computed(() => {
  if (submitting.value) return 'Входим…';
  if (locked.value) return 'Вход заблокирован';
  return 'Войти в портал';
});

const codeClientError = computed(() =>
  codeAttempted.value ? validateLoginCode(code.value) : null,
);
const codeFieldsDisabled = computed(
  () =>
    codeSubmitting.value || portalState.value === 'loading' || resending.value || sessionBusy.value,
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
  const ids = ['seller-email-help'];
  if (emailError.value) ids.push('seller-email-error');
  return ids.join(' ');
});
const passwordDescribedBy = computed(() =>
  passwordError.value ? 'seller-password-error' : undefined,
);
const codeDescribedBy = computed(() => {
  const ids = ['seller-code-help'];
  if (codeClientError.value) ids.push('seller-code-error');
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
  () => session.state.value.generation,
  () => {
    // Own in-flight login also rotates the generation (intent); only a change
    // without a local submission means another tab switched or ended the session.
    if (submitting.value || codeSubmitting.value) return;
    password.value = '';
    email.value = '';
    portalState.value = '';
    shop.value = null;
    resetCodeStep();
  },
  { flush: 'sync' },
);
onBeforeUnmount(() => {
  active = false;
  requestSequence++;
  password.value = '';
});

async function readShop(sequence: number) {
  try {
    const current = await sellerApi.getMyShop();
    if (!active || sequence !== requestSequence) return;
    shop.value = current;
    switch (current.status) {
      case 'approved':
        await router.push('/seller');
        return;
      case 'pending':
        portalState.value = 'pending';
        return;
      case 'rejected':
        portalState.value = 'rejected';
        return;
      case 'blocked':
        portalState.value = 'blocked';
        return;
    }
  } catch (error) {
    if (!active || sequence !== requestSequence) return;
    // Переходный режим MM-84: backend seller.v1 ещё не выкачен (MM-81),
    // поэтому Unimplemented от GetMyShop ведёт в кабинет, как при approved.
    if (error instanceof ConnectError && error.code === Code.Unimplemented) {
      await router.push('/seller');
      return;
    }
    if (sellerErrorReason(error, Code.NotFound, 'SHOP_NOT_FOUND')) {
      portalState.value = 'noShop';
      return;
    }
    portalState.value = 'shopError';
  }
}

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
    const pending = await session.startLogin(address, bytes);
    if (!active || sequence !== requestSequence) return;
    challenge.value = pending;
    step.value = 'code';
    password.value = '';
  } catch (error) {
    if (!active || sequence !== requestSequence) return;
    if (error instanceof ConnectError && error.code === Code.Unimplemented) {
      portalUnavailable.value = true;
    } else if (authErrorReason(error, Code.FailedPrecondition, 'LOGIN_LOCKED')) {
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
    email.value = '';
    portalState.value = 'loading';
    await readShop(sequence);
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
  if (!challenge.value || resending.value || codeSubmitting.value) return;
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

function retryShop() {
  portalState.value = 'loading';
  void readShop(++requestSequence);
}
</script>

<template>
  <section class="auth-layout" aria-labelledby="seller-auth-title">
    <div v-if="portalUnavailable" class="card state-card">
      <h1 id="seller-auth-title" class="state-title">Портал продавца пока недоступен.</h1>
      <p>
        Вход в портал всегда подтверждается кодом из письма, а сервер ещё не принимает такие
        запросы. Мы уже работаем над этим. Попробуйте позже.
      </p>
    </div>

    <template v-else-if="portalState === 'pending'">
      <div class="card state-card">
        <h1 id="seller-auth-title" class="state-title">Заявка на проверке.</h1>
        <p>
          Заявка на подключение магазина ещё на проверке. Мы напишем на вашу рабочую почту, как
          только примем решение.
        </p>
      </div>
    </template>

    <template v-else-if="portalState === 'rejected'">
      <div class="card state-card">
        <h1 id="seller-auth-title" class="state-title">Заявка отклонена.</h1>
        <p v-if="shop?.rejectReason">{{ shop.rejectReason }}</p>
        <p v-if="shop?.fixInstructions">{{ shop.fixInstructions }}</p>
        <RouterLink class="button primary" to="/seller/apply"
          >Подать заявку снова<span aria-hidden="true">↗</span></RouterLink
        >
      </div>
    </template>

    <template v-else-if="portalState === 'blocked'">
      <div class="card state-card">
        <h1 id="seller-auth-title" class="state-title">Доступ к порталу приостановлен.</h1>
        <p>Магазин заблокирован. Напишите в поддержку продавцов, чтобы вернуть магазин в работу.</p>
      </div>
    </template>

    <template v-else-if="portalState === 'noShop'">
      <div class="card state-card">
        <h1 id="seller-auth-title" class="state-title">Магазина пока нет.</h1>
        <p>
          С этой учётной записью не связана заявка на подключение. Отправьте заявку, чтобы начать
          продавать.
        </p>
        <RouterLink class="button primary" to="/seller/apply"
          >Отправить заявку<span aria-hidden="true">↗</span></RouterLink
        >
      </div>
    </template>

    <template v-else-if="portalState === 'shopError'">
      <div class="card state-card">
        <h1 id="seller-auth-title" class="state-title">Не удалось проверить магазин.</h1>
        <p>Вход выполнен, но состояние магазина не загрузилось. Проверьте соединение.</p>
        <button type="button" class="button secondary" @click="retryShop">Проверить снова</button>
      </div>
    </template>

    <div v-else-if="portalState === 'loading'" class="card state-card" aria-busy="true">
      <span class="loading-dot" aria-hidden="true"></span>
      <h1 id="seller-auth-title" class="state-title">Вход подтверждён.</h1>
      <p role="status">Проверяем состояние магазина…</p>
    </div>

    <template v-else>
      <div class="auth-heading">
        <span class="eyebrow">ПОРТАЛ ПРОДАВЦА</span>
        <h1 id="seller-auth-title">
          {{ step === 'code' ? 'Подтвердите вход.' : 'Войдите, чтобы вести магазин.' }}
        </h1>
        <p class="lede">
          {{
            step === 'code'
              ? `Портал продавца всегда спрашивает код. Мы отправили шесть цифр на ${email.trim()}.`
              : 'Доступ к порталу открывается после того, как мы проверим заявку на подключение.'
          }}
        </p>
      </div>

      <form
        v-if="step === 'credentials'"
        class="card auth-card"
        novalidate
        :aria-busy="submitting || sessionBusy"
        @submit.prevent="submitLogin"
      >
        <p v-if="noticeText" class="notice error" role="alert">{{ noticeText }}</p>
        <p v-if="sessionBusy && !submitting" role="status" class="subtle">
          Проверяем сессию перед вводом данных…
        </p>
        <div class="field">
          <label for="seller-email">Рабочая почта</label>
          <input
            id="seller-email"
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
          <span id="seller-email-help" class="field-help"
            >Та же почта, что и в заявке на подключение.</span
          >
          <span v-if="emailError" id="seller-email-error" class="field-error">{{
            emailError
          }}</span>
        </div>
        <div class="field">
          <label for="seller-password">Пароль</label>
          <input
            id="seller-password"
            v-model="password"
            type="password"
            name="password"
            autocomplete="current-password"
            :disabled="fieldsDisabled"
            :aria-invalid="Boolean(passwordError)"
            :aria-describedby="passwordDescribedBy"
          />
          <span v-if="passwordError" id="seller-password-error" class="field-error">{{
            passwordError
          }}</span>
        </div>
        <button class="button primary wide" type="submit" :disabled="fieldsDisabled">
          <span>{{ submitLabel }}</span>
          <span v-if="!submitting && !locked" aria-hidden="true">↗</span>
        </button>
      </form>

      <form
        v-else
        class="card auth-card"
        novalidate
        :aria-busy="codeSubmitting || sessionBusy"
        @submit.prevent="submitCode"
      >
        <p v-if="codeNoticeText" class="notice error" role="alert">{{ codeNoticeText }}</p>
        <p v-if="codeResent" class="notice success" role="status">
          Новый код отправлен на почту. Прежний код больше не подойдёт.
        </p>
        <div class="field">
          <label for="seller-code">Код из письма</label>
          <input
            id="seller-code"
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
          <span id="seller-code-help" class="field-help"
            >Шесть цифр. Код действует
            {{ challenge ? formatCodeTtl(challenge.codeExpiresInSeconds) : 'недолго' }}.</span
          >
          <span v-if="codeClientError" id="seller-code-error" class="field-error">{{
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
      </form>

      <template v-if="step === 'credentials'">
        <p class="auth-alternative">
          Ещё не продаёте у нас? <RouterLink to="/seller/apply">Отправьте заявку</RouterLink>
        </p>
        <p class="auth-alternative">
          Вы покупатель? <RouterLink to="/login">Войдите в личный кабинет</RouterLink>
        </p>
      </template>
    </template>
  </section>
</template>
