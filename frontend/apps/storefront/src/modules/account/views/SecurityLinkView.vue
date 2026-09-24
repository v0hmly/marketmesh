<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Code } from '@connectrpc/connect';
import { authErrorReason } from '../../../shared/api/errors';
import { useSession } from '../../../shell/context';
import {
  validateEmail,
  validatePasswordStrength,
  validatePasswordRepeat,
} from '../../../shared/validation';
import { createSecurityApi, securityError } from '../security/api';
import PasswordRules from '../components/PasswordRules.vue';

const route = useRoute();
const router = useRouter();
const session = useSession();
const api = createSecurityApi();
const token = ref('');
const email = ref('');
const password = ref('');
const repeat = ref('');
const busy = ref(false);
const attempted = ref(false);
const failure = ref('');
const feedback = ref('');
const succeeded = ref(false);
const expired = ref(false);
let active = true;
let revision = 0;
let scrubbingPath: string | null = null;
const action = computed(() => String(route.params.action));
const requesting = computed(() => !token.value && ['verify', 'reset'].includes(action.value));
const title = computed(
  () =>
    ({
      verify: 'Подтвердите почту.',
      reset: 'Восстановите доступ.',
      change_email: 'Подтвердите новую почту.',
      cancel_email: 'Отмените смену почты.',
    })[action.value] ?? 'Проверьте ссылку.',
);
const emailError = computed(() =>
  attempted.value && requesting.value ? validateEmail(email.value) : null,
);
const passwordError = computed(() =>
  attempted.value && !requesting.value && action.value === 'reset'
    ? (validatePasswordStrength(password.value) ??
      validatePasswordRepeat(password.value, repeat.value))
    : null,
);
function captureLink() {
  revision++;
  busy.value = false;
  token.value = '';
  password.value = repeat.value = '';
  attempted.value = succeeded.value = expired.value = false;
  failure.value = feedback.value = '';
  const params = new URLSearchParams(route.hash.slice(1));
  const value = params.get('token') ?? '';
  if (/^[0-9a-f]{32}\.[A-Za-z0-9_-]{43}$/.test(value)) token.value = value;
  else if (route.hash)
    failure.value = 'Ссылка повреждена. Действие не выполнено. Запросите новое письмо.';
  // Keep the token only in this component's memory; remove it from the address and history.
  if (route.hash) {
    scrubbingPath = route.path;
    void router.replace({ path: route.path, hash: '' });
  }
}
watch(
  () => [route.path, route.hash],
  ([path, hash]) => {
    if (path === scrubbingPath && hash === '') {
      scrubbingPath = null;
      return;
    }
    scrubbingPath = null;
    captureLink();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  revision++;
  token.value = password.value = repeat.value = '';
});
function requestNewLink() {
  if (busy.value || !expired.value || !['verify', 'reset'].includes(action.value)) return;
  revision++;
  token.value = password.value = repeat.value = '';
  failure.value = feedback.value = '';
  expired.value = attempted.value = succeeded.value = false;
}
async function submit() {
  if (busy.value || succeeded.value) return;
  attempted.value = true;
  if (emailError.value || passwordError.value) return;
  const selected = action.value;
  const request = requesting.value;
  const secret = token.value;
  if (!request && !secret) {
    failure.value =
      'В ссылке нет кода подтверждения. Действие не выполнено. Откройте ссылку из письма.';
    return;
  }
  const attempt = revision;
  const address = email.value.trim();
  const bytes = new TextEncoder().encode(password.value);
  busy.value = true;
  failure.value = '';
  try {
    if (request) {
      if (selected === 'verify') await api.requestEmailVerification({ email: address });
      else await api.requestPasswordReset({ email: address });
    } else if (selected === 'verify') {
      const signedIn = await session.confirmEmail(secret);
      if (!active || attempt !== revision) return;
      if (signedIn) {
        token.value = '';
        succeeded.value = true;
        await router.replace('/account');
        return;
      }
    } else if (selected === 'reset')
      await session.endSession(() =>
        api.confirmPasswordReset({ token: secret, newPassword: bytes }),
      );
    else if (selected === 'change_email')
      await session.endSession(() => api.confirmEmailChange({ token: secret }));
    else if (selected === 'cancel_email') await api.cancelEmailChange({ token: secret });
    else throw new Error('unsupported action');
    if (!active || attempt !== revision) return;
    succeeded.value = true;
    token.value = '';
    feedback.value = request
      ? 'Если для этой почты доступно действие, письмо отправлено. Проверьте входящие.'
      : selected === 'reset'
        ? 'Пароль изменён. Все сеансы закрыты. Войдите с новым паролем.'
        : selected === 'cancel_email'
          ? 'Смена почты отменена. Прежний адрес сохранён.'
          : selected === 'change_email'
            ? 'Почта изменена. Войдите с новым адресом.'
            : 'Почта подтверждена. Для входа в этом браузере введите почту, пароль и код из письма.';
  } catch (error) {
    if (active && attempt === revision) {
      expired.value =
        authErrorReason(error, Code.FailedPrecondition, 'TOKEN_EXPIRED') ||
        authErrorReason(error, Code.FailedPrecondition, 'TOKEN_USED');
      failure.value = expired.value
        ? 'Ссылка недействительна или срок её действия истёк. Запросите новую ссылку.'
        : securityError(error);
    }
  } finally {
    bytes.fill(0);
    if (active && attempt === revision) {
      busy.value = false;
      password.value = repeat.value = '';
    }
  }
}
</script>

<template>
  <section class="auth-layout">
    <div class="auth-heading">
      <p class="eyebrow">Безопасность аккаунта</p>
      <h1>{{ title }}</h1>
      <p class="lede">
        {{
          action === 'verify' && token
            ? 'Подтвердите почту — в браузере регистрации мы сразу откроем ваш аккаунт.'
            : 'Ссылка из письма действует один раз. Подтвердите действие на этой странице.'
        }}
      </p>
    </div>
    <form class="card auth-card" novalidate :aria-busy="busy" @submit.prevent="submit">
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <fieldset v-if="!succeeded && !expired" :disabled="busy">
        <legend class="visually-hidden">{{ title }}</legend>
        <div v-if="requesting" class="field">
          <label for="link-email">Почта</label
          ><input
            id="link-email"
            v-model="email"
            type="email"
            autocomplete="email"
            :aria-invalid="!!emailError"
            :aria-describedby="emailError ? 'link-email-error' : undefined"
          /><span v-if="emailError" id="link-email-error" class="field-error">{{
            emailError
          }}</span>
        </div>
        <template v-else-if="action === 'reset' && token">
          <div class="field">
            <label for="reset-password">Новый пароль</label
            ><input
              id="reset-password"
              v-model="password"
              type="password"
              autocomplete="new-password"
              :aria-invalid="!!passwordError"
              :aria-describedby="
                ['reset-help', passwordError && 'reset-error', password && 'reset-rules']
                  .filter(Boolean)
                  .join(' ')
              "
            /><span id="reset-help" class="field-help"
              >От 8 до 64 символов: строчная и заглавная латинские буквы, цифра и спецсимвол.</span
            >
          </div>
          <PasswordRules v-if="password" id="reset-rules" :password="password" />
          <div class="field">
            <label for="reset-repeat">Повторите новый пароль</label
            ><input
              id="reset-repeat"
              v-model="repeat"
              type="password"
              autocomplete="new-password"
              :aria-invalid="!!passwordError"
              :aria-describedby="passwordError ? 'reset-error' : undefined"
            /><span v-if="passwordError" id="reset-error" class="field-error">{{
              passwordError
            }}</span>
          </div>
        </template>
        <button class="button primary wide" :disabled="busy || (!requesting && !token)">
          {{
            busy
              ? 'Проверяем…'
              : requesting
                ? 'Отправить письмо'
                : action === 'reset'
                  ? 'Сохранить новый пароль'
                  : action === 'verify'
                    ? 'Подтвердить почту'
                    : 'Подтвердить действие'
          }}
        </button>
      </fieldset>
      <button
        v-if="expired && ['verify', 'reset'].includes(action)"
        type="button"
        class="button primary wide"
        :disabled="busy"
        @click="requestNewLink"
      >
        Получить новую ссылку
      </button>
      <RouterLink v-else-if="expired" class="button primary wide" to="/account/security">
        Перейти к настройкам безопасности
      </RouterLink>
      <RouterLink class="button text-button" to="/login">Перейти ко входу</RouterLink>
    </form>
  </section>
</template>
