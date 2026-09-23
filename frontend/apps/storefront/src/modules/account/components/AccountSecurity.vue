<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import type { GetCredentialsResponse, SessionInfo } from '../../../shared/api/security';
import { Code, ConnectError } from '@connectrpc/connect';
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
import PasswordRules from './PasswordRules.vue';

/**
 * Вход и безопасность, сеансы и устройства. Разделы экрана MarketMesh ID; без ID —
 * содержимое отдельного экрана. Родитель получает почту и её статус через `credentials`.
 */
const emit = defineEmits<{ credentials: [value: GetCredentialsResponse | null] }>();
const session = useSession();
const api = createSecurityApi();
const credentials = shallowRef<GetCredentialsResponse | null>(null);
const sessions = shallowRef<SessionInfo[]>([]);
const busy = ref(false);
const failure = ref('');
const feedback = ref('');
const opened = ref<'email' | 'password' | 'code' | null>(null);
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
const date = (unix: bigint) =>
  new Date(Number(unix) * 1000).toLocaleString('ru-RU', {
    day: 'numeric',
    month: 'long',
    hour: '2-digit',
    minute: '2-digit',
  });
function sessionMeta(item: SessionInfo) {
  const parts = [item.location.trim() || 'Место не определено'];
  if (item.lastSeenAtUnix > 0n) parts.push(`активность ${date(item.lastSeenAtUnix)}`);
  if (item.createdAtUnix > 0n) parts.push(`вход ${date(item.createdAtUnix)}`);
  return parts.join(' · ');
}
function deviceName(item: SessionInfo) {
  return (
    [item.device.trim(), item.browser.trim()].filter(Boolean).join(', ') ||
    'Устройство не определено'
  );
}
let active = true;
let revision = 0;
let lastFailure: unknown;
function clearSecrets() {
  currentPassword.value =
    newPassword.value =
    repeatPassword.value =
    emailPassword.value =
    newEmail.value =
    codePassword.value =
    code.value =
      '';
  attempted.value = '';
}
function erase() {
  revision++;
  credentials.value = null;
  sessions.value = [];
  opened.value = null;
  clearSecrets();
  challenge.value = null;
}
async function open(form: 'email' | 'password' | 'code' | null) {
  if (busy.value) return;
  const previous = opened.value;
  clearSecrets();
  opened.value = form;
  failure.value = feedback.value = '';
  await nextTick();
  if (form) document.getElementById(`security-${form}-first`)?.focus();
  else if (previous) document.getElementById(`security-${previous}-toggle`)?.focus();
}
watch(credentials, (value) => emit('credentials', value));
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
  const fetch = () =>
    session.withSession(session.capture(), () =>
      Promise.all([api.getCredentials({}), api.listSessions({})]),
    );
  try {
    let result: Awaited<ReturnType<typeof fetch>>;
    try {
      result = await fetch();
    } catch (error) {
      // Only reads may recover and retry once after the access cookie expires.
      if (!(error instanceof ConnectError && error.code === Code.Unauthenticated)) throw error;
      await session.bootstrap();
      if (!active || !authenticated.value) return;
      attempt = revision;
      result = await fetch();
    }
    if (!active || attempt !== revision) return;
    credentials.value = result[0];
    sessions.value = result[1].sessions;
  } catch (error) {
    if (active && attempt === revision) failure.value = securityError(error);
  } finally {
    if (active) {
      busy.value = false;
      // A read that outlived an owner refresh is discarded; the same owner gets a fresh one.
      if (attempt !== revision && authenticated.value && !credentials.value) void read();
    }
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
    const result = await run(
      () => api.startEmailChange({ newEmail: email, password }),
      'Письмо подтверждения отправлено на новую почту, ссылка отмены — на прежнюю.',
    );
    if (result && active) {
      opened.value = null;
      newEmail.value = '';
      attempted.value = '';
      await nextTick();
      document.getElementById('security-email-toggle')?.focus();
    }
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
      opened.value = null;
      await nextTick();
      document.getElementById('security-code')?.focus();
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
  const result = await run(
    () => api.revokeSession({ sessionId: item.sessionId }),
    `Сеанс «${deviceName(item)}» завершён.`,
  );
  // После ошибки список не перечитываем: иначе сообщение об ошибке пропало бы.
  if (result !== undefined) await read();
}
async function logoutAll() {
  await run(
    () => api.logoutAll({}),
    'Все сеансы закрыты, включая этот. Войдите снова, чтобы продолжить.',
    true,
  );
}
void read();
</script>

<template>
  <div
    id="security"
    class="security-sections"
    role="group"
    aria-label="Вход, безопасность и сеансы"
    tabindex="-1"
  >
    <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
    <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
    <template v-if="authenticated">
      <div v-if="!credentials" class="card state-card" :aria-busy="busy">
        <p v-if="busy" role="status">Загружаем вход и сеансы…</p>
        <template v-else
          ><h2>Вход и сеансы пока недоступны</h2>
          <button class="button secondary" @click="read">Повторить загрузку</button></template
        >
      </div>
      <template v-else>
        <section class="card profile-card security-card" aria-labelledby="security-title">
          <div>
            <h2 id="security-title">Вход и безопасность</h2>
            <p class="subtle">Почта и пароль для входа, подтверждение входа кодом из письма.</p>
          </div>
          <div class="security-row">
            <div class="security-row-text">
              <span class="security-row-title">Почта для входа</span>
              <span class="subtle"
                >{{ credentials.email
                }}{{ credentials.emailVerified ? '' : ' · не подтверждена' }}</span
              >
            </div>
            <button
              v-if="opened !== 'email'"
              id="security-email-toggle"
              class="button secondary"
              :disabled="busy || Boolean(challenge)"
              @click="open('email')"
            >
              Сменить почту
            </button>
          </div>
          <form
            v-if="opened === 'email'"
            class="security-form"
            novalidate
            :aria-busy="busy"
            @submit.prevent="changeEmail"
          >
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Смена почты</legend>
              <div class="field">
                <label for="security-email-first">Новая почта</label
                ><input
                  id="security-email-first"
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
              <div class="button-row">
                <button type="button" class="button text-button" @click="open(null)">
                  Отменить</button
                ><button class="button primary" :disabled="busy || !emailPassword">
                  Отправить подтверждение <span aria-hidden="true">↗</span>
                </button>
              </div>
            </fieldset>
          </form>
          <div class="security-row">
            <div class="security-row-text">
              <span class="security-row-title">Пароль</span>
              <span class="subtle">Нужен для входа и подтверждения важных изменений.</span>
            </div>
            <button
              v-if="opened !== 'password'"
              id="security-password-toggle"
              class="button secondary"
              :disabled="busy || Boolean(challenge)"
              @click="open('password')"
            >
              Сменить пароль
            </button>
          </div>
          <form
            v-if="opened === 'password'"
            class="security-form"
            novalidate
            :aria-busy="busy"
            @submit.prevent="changePassword"
          >
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Смена пароля</legend>
              <div class="field">
                <label for="security-password-first">Текущий пароль</label
                ><input
                  id="security-password-first"
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
                  >От 8 до 64 символов: строчная и заглавная латинские буквы, цифра и спецсимвол.
                  После смены закроем все сеансы.</span
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
              <div class="button-row">
                <button type="button" class="button text-button" @click="open(null)">
                  Отменить</button
                ><button class="button primary" :disabled="busy || !currentPassword">
                  Изменить пароль <span aria-hidden="true">↗</span>
                </button>
              </div>
            </fieldset>
          </form>
          <div class="security-row">
            <div class="security-row-text">
              <span class="security-row-title">Код при входе</span>
              <span class="subtle">{{
                credentials.loginCodeEnabled
                  ? 'После пароля требуется код из письма.'
                  : 'Вы входите с почтой и паролем.'
              }}</span>
            </div>
            <button
              v-if="opened !== 'code' && !challenge"
              id="security-code-toggle"
              class="button secondary"
              :disabled="busy"
              @click="open('code')"
            >
              {{ credentials.loginCodeEnabled ? 'Отключить' : 'Включить' }}
            </button>
          </div>
          <form
            v-if="opened === 'code' && !challenge"
            class="security-form"
            novalidate
            :aria-busy="busy"
            @submit.prevent="startCode"
          >
            <fieldset :disabled="busy">
              <legend class="visually-hidden">Подтверждение входа</legend>
              <div class="field">
                <label for="security-code-first">Пароль для настройки входа</label
                ><input
                  id="security-code-first"
                  v-model="codePassword"
                  type="password"
                  autocomplete="current-password"
                  aria-describedby="code-policy-help"
                /><span id="code-policy-help" class="field-help"
                  >Изменение нужно подтвердить кодом из письма. Все сеансы будут закрыты.</span
                >
              </div>
              <div class="button-row">
                <button type="button" class="button text-button" @click="open(null)">
                  Отменить</button
                ><button class="button primary" :disabled="busy || !codePassword">
                  {{
                    credentials.loginCodeEnabled
                      ? 'Отключить подтверждение входа'
                      : 'Включить подтверждение входа'
                  }}
                  <span aria-hidden="true">↗</span>
                </button>
              </div>
            </fieldset>
          </form>
          <form
            v-if="challenge"
            class="security-form"
            novalidate
            :aria-busy="busy"
            @submit.prevent="confirmCode"
          >
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
              <button class="button primary" :disabled="busy">
                Подтвердить настройку <span aria-hidden="true">↗</span>
              </button>
            </fieldset>
          </form>
        </section>
        <section class="card profile-card security-card" aria-labelledby="sessions-title">
          <div>
            <h2 id="sessions-title">Сеансы и устройства</h2>
            <p class="subtle">
              Вы заходили в аккаунт с этих устройств. Город определяется по IP и может быть
              неточным.
            </p>
          </div>
          <p v-if="cooldown" class="notice error">
            Вы вошли с нового устройства. До {{ date(credentials.newDeviceCooldownUntilUnix) }}
            другие сеансы завершать нельзя — так мы защищаем аккаунт, если устройство не ваше.
          </p>
          <ul class="session-list" aria-label="Сеансы">
            <li
              v-for="item in sessions"
              :key="Array.from(item.sessionId).join(',')"
              class="session-row"
            >
              <div class="security-row-text">
                <span class="session-device"
                  >{{ deviceName(item)
                  }}<span v-if="item.current" class="draft-badge">Текущий сеанс</span></span
                >
                <span class="subtle">{{ sessionMeta(item) }}</span>
              </div>
              <button
                v-if="!item.current"
                class="button text-button"
                :disabled="busy || !!cooldown"
                @click="revoke(item)"
              >
                Завершить<span class="visually-hidden"> сеанс «{{ deviceName(item) }}»</span>
              </button>
            </li>
          </ul>
          <div class="security-row sessions-footer">
            <p id="logout-all-help" class="subtle">
              Закроем все сеансы, включая этот. После этого нужно будет войти заново.
            </p>
            <button
              class="button secondary"
              :disabled="busy || !!cooldown"
              aria-describedby="logout-all-help"
              @click="logoutAll"
            >
              Выйти на всех устройствах
            </button>
          </div>
        </section>
      </template>
    </template>
  </div>
</template>
