<script setup lang="ts">
import '../../auth/forms';
import '../style.css';
import { computed, onBeforeUnmount, ref } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { useSellerApi } from '../context';
import { useSession } from '../../../shell/context';
import { sellerErrorReason } from '../errors';
import { trimDisplayName } from '../../../shared/validation';
import {
  passwordChecks,
  validateEmail,
  validatePasswordRepeat,
  validatePasswordStrength,
} from '../../auth/public';

const session = useSession();
const sellerApi = useSellerApi();

const email = ref('');
const password = ref('');
const confirmPassword = ref('');
const shopName = ref('');
const inn = ref('');
const attempted = ref(false);
const passwordStarted = ref(false);

const submitting = ref(false);
const succeeded = ref(false);
const serverFailure = ref<
  '' | 'emailTaken' | 'pendingAlready' | 'innTaken' | 'nameTaken' | 'serverError'
>('');
/** Backend seller.v1 ещё не выкачен (MM-81): приём заявок недоступен. */
const unavailable = ref(false);

let requestSequence = 0;
let active = true;

const SHOP_NAME_MAX = 80;

const sessionBusy = computed(() =>
  ['unknown', 'checking', 'switching', 'signingOut', 'unsupported'].includes(
    session.state.value.status,
  ),
);

const emailError = computed(() => {
  if (serverFailure.value === 'emailTaken')
    return 'Эта почта уже зарегистрирована. Войдите в портал или восстановите пароль.';
  if (serverFailure.value === 'pendingAlready')
    return 'Заявка с этой почтой уже на проверке. Мы напишем, как только примем решение.';
  return attempted.value ? validateEmail(email.value) : null;
});
const passwordError = computed(() =>
  attempted.value ? validatePasswordStrength(password.value) : null,
);
const confirmError = computed(() =>
  attempted.value ? validatePasswordRepeat(password.value, confirmPassword.value) : null,
);
const requirements = computed(() => passwordChecks(password.value));

const shopNameTrimmed = computed(() => trimDisplayName(shopName.value));
const shopNameCount = computed(() => Array.from(shopName.value).length);
const shopError = computed(() => {
  if (serverFailure.value === 'nameTaken')
    return 'Такое название уже занято другим магазином. Выберите другое.';
  if (!attempted.value) return null;
  if (!shopNameTrimmed.value) return 'Введите название магазина.';
  if (Array.from(shopNameTrimmed.value).length < 2)
    return 'Название слишком короткое. Нужно хотя бы два символа.';
  if (shopNameCount.value > SHOP_NAME_MAX) return 'Название слишком длинное. Не более 80 символов.';
  return null;
});
const innError = computed(() => {
  if (serverFailure.value === 'innTaken')
    return 'На этот ИНН уже зарегистрирован магазин. Войдите в существующий аккаунт или напишите в поддержку продавцов.';
  if (!attempted.value) return null;
  if (!inn.value) return 'Введите ИНН.';
  if (inn.value.length !== 10 && inn.value.length !== 12) return 'ИНН состоит из 10 или 12 цифр.';
  return null;
});

const fieldsDisabled = computed(() => submitting.value || succeeded.value || sessionBusy.value);
const noticeText = computed(() =>
  serverFailure.value === 'serverError'
    ? 'Не удалось отправить заявку. Проверьте соединение и попробуйте ещё раз.'
    : '',
);
const submitLabel = computed(() => (submitting.value ? 'Отправляем заявку…' : 'Отправить заявку'));

const emailDescribedBy = computed(() => {
  const ids = ['apply-email-help'];
  if (emailError.value) ids.push('apply-email-error');
  return ids.join(' ');
});
const passwordDescribedBy = computed(() => {
  const ids: string[] = [];
  if (passwordStarted.value) ids.push('apply-password-requirements');
  if (passwordError.value) ids.push('apply-password-error');
  return ids.join(' ') || undefined;
});
const confirmDescribedBy = computed(() => (confirmError.value ? 'apply-confirm-error' : undefined));
const shopDescribedBy = computed(() => {
  const ids = ['apply-shop-help', 'apply-shop-count'];
  if (shopError.value) ids.push('apply-shop-error');
  return ids.join(' ');
});
const innDescribedBy = computed(() => {
  const ids = ['apply-inn-help'];
  if (innError.value) ids.push('apply-inn-error');
  return ids.join(' ');
});

function onInnInput(event: Event) {
  inn.value = (event.target as HTMLInputElement).value.replace(/\D/g, '').slice(0, 12);
  if (serverFailure.value === 'innTaken') serverFailure.value = '';
}

function onInput() {
  serverFailure.value = '';
}

function onPasswordInput() {
  passwordStarted.value = true;
  onInput();
}

onBeforeUnmount(() => {
  active = false;
  requestSequence++;
  password.value = '';
  confirmPassword.value = '';
});

async function submit() {
  if (fieldsDisabled.value) return;
  attempted.value = true;
  serverFailure.value = '';
  if (
    emailError.value ||
    passwordError.value ||
    confirmError.value ||
    shopError.value ||
    innError.value
  )
    return;
  submitting.value = true;
  const sequence = ++requestSequence;
  const bytes = new TextEncoder().encode(password.value);
  try {
    await sellerApi.submitApplication({
      email: email.value.trim(),
      password: bytes,
      shopName: shopNameTrimmed.value,
      inn: inn.value,
    });
    if (!active || sequence !== requestSequence) return;
    succeeded.value = true;
    password.value = '';
    confirmPassword.value = '';
  } catch (error) {
    if (!active || sequence !== requestSequence) return;
    if (sellerErrorReason(error, Code.FailedPrecondition, 'EMAIL_TAKEN'))
      serverFailure.value = 'emailTaken';
    else if (sellerErrorReason(error, Code.FailedPrecondition, 'APPLICATION_PENDING'))
      serverFailure.value = 'pendingAlready';
    else if (sellerErrorReason(error, Code.FailedPrecondition, 'INN_TAKEN'))
      serverFailure.value = 'innTaken';
    else if (sellerErrorReason(error, Code.FailedPrecondition, 'NAME_TAKEN'))
      serverFailure.value = 'nameTaken';
    else if (error instanceof ConnectError && error.code === Code.Unimplemented)
      unavailable.value = true;
    else serverFailure.value = 'serverError';
  } finally {
    bytes.fill(0);
    submitting.value = false;
  }
}
</script>

<template>
  <section class="auth-layout" aria-labelledby="apply-title">
    <div v-if="unavailable" class="card state-card">
      <h1 id="apply-title" class="state-title">Приём заявок пока недоступен.</h1>
      <p>
        Мы ещё запускаем портал продавца, и заявки временно не принимаются. Мы уже работаем над
        этим. Вернитесь позже.
      </p>
    </div>

    <template v-else-if="succeeded">
      <div class="card state-card">
        <h1 id="apply-title" class="state-title">Заявка отправлена.</h1>
        <p>
          Проверяем данные магазина и напишем на {{ email.trim() }}. Обычно проверка занимает
          несколько дней.
        </p>
        <p class="subtle">
          Войти в портал можно уже сейчас — раздел с товарами откроется после проверки.
        </p>
        <RouterLink class="button primary" to="/seller/login"
          >Перейти к входу<span aria-hidden="true">↗</span></RouterLink
        >
      </div>
    </template>

    <template v-else>
      <div class="auth-heading">
        <span class="eyebrow">ПОРТАЛ ПРОДАВЦА</span>
        <h1 id="apply-title">Расскажите о магазине.</h1>
        <p class="lede">
          Проверим данные и откроем доступ к порталу. Аккаунт создаётся сразу, работа с товарами —
          после проверки.
        </p>
      </div>

      <form
        class="card auth-card"
        novalidate
        :aria-busy="submitting || sessionBusy"
        @submit.prevent="submit"
      >
        <p v-if="noticeText" class="notice error" role="alert">{{ noticeText }}</p>
        <p v-if="sessionBusy && !submitting" role="status" class="subtle">
          Проверяем сессию перед вводом данных…
        </p>
        <div class="field">
          <label for="apply-email">Рабочая почта</label>
          <input
            id="apply-email"
            v-model="email"
            type="email"
            name="email"
            autocomplete="username"
            autocapitalize="none"
            spellcheck="false"
            :disabled="fieldsDisabled"
            :aria-invalid="Boolean(emailError)"
            :aria-describedby="emailDescribedBy"
            @input="onInput"
          />
          <span id="apply-email-help" class="field-help"
            >Логин для портала. На неё придёт решение по заявке.</span
          >
          <span v-if="emailError" id="apply-email-error" class="field-error">{{ emailError }}</span>
        </div>
        <div class="field">
          <label for="apply-password">Пароль</label>
          <input
            id="apply-password"
            v-model="password"
            type="password"
            name="password"
            autocomplete="new-password"
            :disabled="fieldsDisabled"
            :aria-invalid="Boolean(passwordError)"
            :aria-describedby="passwordDescribedBy"
            @input="onPasswordInput"
          />
          <ul v-if="passwordStarted" id="apply-password-requirements" class="password-requirements">
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
          <span v-if="passwordError" id="apply-password-error" class="field-error">{{
            passwordError
          }}</span>
        </div>
        <div class="field">
          <label for="apply-confirm">Повторите пароль</label>
          <input
            id="apply-confirm"
            v-model="confirmPassword"
            type="password"
            name="confirmPassword"
            autocomplete="new-password"
            :disabled="fieldsDisabled"
            :aria-invalid="Boolean(confirmError)"
            :aria-describedby="confirmDescribedBy"
            @input="onInput"
          />
          <span v-if="confirmError" id="apply-confirm-error" class="field-error">{{
            confirmError
          }}</span>
        </div>
        <div class="field">
          <div class="label-line">
            <label for="apply-shop">Название магазина</label>
            <span class="field-count" id="apply-shop-count"
              >{{ shopNameCount }} / {{ SHOP_NAME_MAX }}</span
            >
          </div>
          <input
            id="apply-shop"
            v-model="shopName"
            type="text"
            name="shopName"
            :maxlength="SHOP_NAME_MAX"
            :disabled="fieldsDisabled"
            :aria-invalid="Boolean(shopError)"
            :aria-describedby="shopDescribedBy"
            @input="onInput"
          />
          <span id="apply-shop-help" class="field-help"
            >Так магазин увидят покупатели в каталоге.</span
          >
          <span v-if="shopError" id="apply-shop-error" class="field-error">{{ shopError }}</span>
        </div>
        <div class="field">
          <label for="apply-inn">ИНН</label>
          <input
            id="apply-inn"
            :value="inn"
            type="text"
            name="inn"
            inputmode="numeric"
            maxlength="12"
            :disabled="fieldsDisabled"
            :aria-invalid="Boolean(innError)"
            :aria-describedby="innDescribedBy"
            @input="onInnInput"
          />
          <span id="apply-inn-help" class="field-help"
            >10 цифр для организации, 12 — для ИП. Без пробелов.</span
          >
          <span v-if="innError" id="apply-inn-error" class="field-error">{{ innError }}</span>
        </div>
        <button class="button primary wide" type="submit" :disabled="fieldsDisabled">
          <span>{{ submitLabel }}</span>
          <span v-if="!submitting" aria-hidden="true">↗</span>
        </button>
      </form>

      <p class="auth-alternative">
        Заявка уже отправлена? <RouterLink to="/seller/login">Войдите в портал</RouterLink>
      </p>
    </template>
  </section>
</template>
