<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { useSession } from '../../../shell/context';
import { accountError } from '../errors';
import { analytics } from '../../../shared/analytics';
import { validateCredentials } from '../validation';

const props = defineProps<{ mode: 'login' | 'register' }>();
const session = useSession();
const router = useRouter();
const identifier = ref('');
const password = ref('');
const submitting = ref(false);
const errors = ref<{ identifier?: string; password?: string }>({});
const failure = ref('');
const registered = ref(false);
let requestSequence = 0;
let active = true;
const isRegister = computed(() => props.mode === 'register');
const disabled = computed(
  () =>
    submitting.value ||
    ['unknown', 'checking', 'switching', 'signingOut', 'unsupported'].includes(
      session.state.value.status,
    ),
);

watch(
  () => props.mode,
  () => {
    requestSequence++;
    password.value = '';
    failure.value = '';
    errors.value = {};
  },
);
watch(
  () => session.state.value.generation,
  () => {
    password.value = '';
    identifier.value = '';
  },
  { flush: 'sync' },
);
onBeforeUnmount(() => {
  active = false;
  requestSequence++;
  password.value = '';
  identifier.value = '';
});

async function submit() {
  if (disabled.value) return;
  errors.value = validateCredentials(identifier.value, password.value);
  failure.value = '';
  if (Object.keys(errors.value).length) return;
  submitting.value = true;
  const sequence = ++requestSequence;
  const bytes = new TextEncoder().encode(password.value);
  password.value = '';
  try {
    if (isRegister.value) {
      await session.register(identifier.value, bytes);
      if (!active || sequence !== requestSequence) return;
      analytics.event('registration_request_completed');
      registered.value = true;
      await router.push('/login');
    } else {
      await session.login(identifier.value, bytes);
      if (!active || sequence !== requestSequence) return;
      identifier.value = '';
      analytics.event('login_succeeded');
      await router.push('/account');
    }
  } catch (error) {
    if (active && sequence === requestSequence) failure.value = accountError(error);
  } finally {
    bytes.fill(0);
    password.value = '';
    submitting.value = false;
  }
}
</script>

<template>
  <section class="auth-layout" aria-labelledby="auth-title">
    <div class="auth-intro">
      <span class="eyebrow">MARKETMESH · ЛИЧНОЕ ПРОСТРАНСТВО</span>
      <h1 id="auth-title">{{ isRegister ? 'Начнём знакомство.' : 'Рады вас видеть.' }}</h1>
      <p class="lede">
        {{
          isRegister
            ? 'Создайте аккаунт, чтобы сохранить своё имя и рассказать немного о себе.'
            : 'Войдите в аккаунт, чтобы открыть свой профиль.'
        }}
      </p>
      <div class="auth-note">
        <span class="note-mark" aria-hidden="true">✳</span>
        <p>Вещи с характером.<br />Люди, которые их создают.</p>
      </div>
    </div>
    <div class="card auth-card">
      <p class="eyebrow">{{ isRegister ? 'НОВЫЙ АККАУНТ' : 'ВАШ АККАУНТ' }}</p>
      <h2>{{ isRegister ? 'Регистрация' : 'Вход' }}</h2>
      <p v-if="registered && !isRegister" class="notice success" role="status">
        Запрос обработан. Теперь войдите с вашим логином и паролем.
      </p>
      <p
        v-if="['unknown', 'checking'].includes(session.state.value.status)"
        role="status"
        class="subtle"
      >
        Проверяем сессию перед вводом данных…
      </p>
      <form
        novalidate
        :aria-busy="submitting || ['unknown', 'checking'].includes(session.state.value.status)"
        @submit.prevent="submit"
      >
        <div class="field">
          <label for="identifier">Логин</label>
          <input
            id="identifier"
            v-model="identifier"
            name="username"
            autocomplete="username"
            autocapitalize="none"
            spellcheck="false"
            required
            :disabled="disabled"
            :aria-invalid="Boolean(errors.identifier)"
            :aria-describedby="
              errors.identifier ? 'identifier-error identifier-help' : 'identifier-help'
            "
          />
          <span id="identifier-help" class="field-help">Без пробелов. Регистр не важен.</span>
          <span v-if="errors.identifier" id="identifier-error" class="field-error">{{
            errors.identifier
          }}</span>
        </div>
        <div class="field">
          <label for="password">Пароль</label>
          <input
            id="password"
            v-model="password"
            name="password"
            type="password"
            :autocomplete="isRegister ? 'new-password' : 'current-password'"
            required
            :disabled="disabled"
            :aria-invalid="Boolean(errors.password)"
            :aria-describedby="errors.password ? 'password-help password-error' : 'password-help'"
          />
          <span id="password-help" class="field-help">От 8 до 64 символов.</span>
          <span v-if="errors.password" id="password-error" class="field-error">{{
            errors.password
          }}</span>
        </div>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button primary wide" type="submit" :disabled="disabled">
          {{ submitting ? 'Подождите…' : isRegister ? 'Создать аккаунт' : 'Войти'
          }}<span aria-hidden="true">↗</span>
        </button>
      </form>
      <p class="auth-alternative">
        {{ isRegister ? 'Уже есть аккаунт?' : 'Впервые здесь?' }}
        <RouterLink :to="isRegister ? '/login' : '/register'">{{
          isRegister ? 'Войти' : 'Зарегистрироваться'
        }}</RouterLink>
      </p>
    </div>
  </section>
</template>
