<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { Code } from '@connectrpc/connect';
import { useSession } from '../../../shell/context';
import { authErrorReason } from '../../../shared/api/errors';
import { normalizeCodeInput, validateLoginCode } from '../../../shared/validation';
import { createSecurityApi, securityError } from '../security/api';

const props = defineProps<{ remaining: number; disabled: boolean }>();
const emit = defineEmits<{ generated: [remaining: number] }>();
const session = useSession();
const api = createSecurityApi();
const password = ref('');
const code = ref('');
const challenge = shallowRef<Uint8Array | null>(null);
const codes = shallowRef<string[]>([]);
const busy = ref(false);
const failure = ref('');
const attempted = ref(false);
const codeError = computed(() => (attempted.value ? validateLoginCode(code.value) : null));
const disabled = computed(
  () => props.disabled || busy.value || session.state.value.status !== 'authenticated',
);
let active = true;
let revision = 0;
function erase() {
  revision++;
  password.value = code.value = '';
  codes.value.fill('');
  codes.value = [];
  challenge.value = null;
  attempted.value = false;
}
watch(
  () => [session.state.value.status, session.state.value.generation, session.state.value.subjectId],
  erase,
  { flush: 'sync' },
);
onBeforeUnmount(() => {
  active = false;
  erase();
});
async function start() {
  if (disabled.value || !password.value) return;
  const owner = session.capture();
  const attempt = revision;
  const bytes = new TextEncoder().encode(password.value);
  password.value = '';
  failure.value = '';
  busy.value = true;
  try {
    const result = await session.withSession(owner, () =>
      api.startRecoveryCodes({ password: bytes }),
    );
    if (!active || attempt !== revision) return;
    if (result.challengeId.length !== 16 || result.codeExpiresInSeconds < 1n)
      throw new Error('invalid recovery challenge');
    challenge.value = result.challengeId;
  } catch (error) {
    if (active && attempt === revision) failure.value = securityError(error);
  } finally {
    bytes.fill(0);
    if (active) busy.value = false;
  }
}
async function confirm() {
  if (disabled.value || !challenge.value) return;
  attempted.value = true;
  if (codeError.value) return;
  const owner = session.capture();
  const attempt = revision;
  const id = challenge.value;
  const value = code.value;
  busy.value = true;
  failure.value = '';
  try {
    // Mutations are never replayed after refresh or an unknown transport outcome.
    const result = await session.withSession(owner, () =>
      api.completeRecoveryCodes({ challengeId: id, code: value }),
    );
    if (!active || attempt !== revision) {
      result.codes.fill('');
      return;
    }
    if (
      result.codes.length !== 8 ||
      new Set(result.codes).size !== 8 ||
      result.codes.some((item) => !/^[a-f0-9]{8}(?:-[a-f0-9]{8}){3}$/.test(item))
    ) {
      result.codes.fill('');
      throw new Error('invalid recovery set');
    }
    codes.value = [...result.codes];
    result.codes.fill('');
    challenge.value = null;
    emit('generated', 8);
  } catch (error) {
    if (!active || attempt !== revision) return;
    if (
      authErrorReason(error, Code.InvalidArgument, 'CODE_MISMATCH') ||
      authErrorReason(error, Code.FailedPrecondition, 'CODE_REISSUED') ||
      authErrorReason(error, Code.ResourceExhausted, 'RATE_LIMITED')
    ) {
      failure.value = securityError(error);
    } else {
      challenge.value = null;
      failure.value =
        'Набор не получен. Он мог быть создан, но показать его повторно нельзя. Создайте новый набор с подтверждением по почте.';
    }
  } finally {
    code.value = '';
    attempted.value = false;
    if (active) busy.value = false;
  }
}
</script>

<template>
  <section class="card profile-card" aria-labelledby="recovery-title">
    <h2 id="recovery-title">Резервные коды.</h2>
    <p>Осталось кодов: {{ remaining }} из 8.</p>
    <p class="field-help">
      Для входа без доступа к почте понадобятся пароль и один сохранённый код. Смена пароля, почты
      или настройки подтверждения входа отменяет набор.
    </p>
    <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
    <template v-if="codes.length">
      <p class="notice success" role="status">
        Новый набор создан. Прежние коды больше не действуют.
      </p>
      <p id="recovery-save-help" class="field-help">
        Сохраните эти 8 кодов в надёжном месте. Они показаны только сейчас и исчезнут при закрытии
        страницы или смене сеанса. Мы не отправляем их письмом.
      </p>
      <ol aria-label="Новый набор резервных кодов" aria-describedby="recovery-save-help">
        <li v-for="(item, index) in codes" :key="index">
          <code>{{ item }}</code>
        </li>
      </ol>
      <button type="button" class="button secondary" @click="erase">Скрыть сохранённые коды</button>
    </template>
    <form v-else-if="challenge" novalidate :aria-busy="busy" @submit.prevent="confirm">
      <fieldset :disabled="disabled">
        <legend class="visually-hidden">Подтверждение генерации резервных кодов</legend>
        <div class="field">
          <label for="recovery-email-code">Код для резервного набора</label>
          <input
            id="recovery-email-code"
            :value="code"
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="6"
            :aria-invalid="!!codeError"
            :aria-describedby="
              codeError ? 'recovery-code-help recovery-code-error' : 'recovery-code-help'
            "
            @input="code = normalizeCodeInput(($event.target as HTMLInputElement).value)"
          />
          <span id="recovery-code-help" class="field-help"
            >Шесть цифр из последнего письма. Код действует 10 минут. После трёх ошибок придёт
            новый.</span
          >
          <span v-if="codeError" id="recovery-code-error" class="field-error">{{ codeError }}</span>
        </div>
        <button class="button primary">{{ busy ? 'Создаём коды…' : 'Создать новый набор' }}</button>
        <button type="button" class="button text-button" @click="erase">Отменить генерацию</button>
      </fieldset>
    </form>
    <form v-else novalidate :aria-busy="busy" @submit.prevent="start">
      <fieldset :disabled="disabled">
        <legend class="visually-hidden">Создание резервных кодов</legend>
        <div class="field">
          <label for="recovery-password">Пароль для резервных кодов</label>
          <input
            id="recovery-password"
            v-model="password"
            type="password"
            autocomplete="current-password"
            aria-describedby="recovery-password-help"
          />
          <span id="recovery-password-help" class="field-help"
            >Подтвердите пароль, затем введите код из письма. После создания прежний набор станет
            недействительным.</span
          >
        </div>
        <button class="button primary" :disabled="disabled || !password">
          {{ busy ? 'Отправляем код…' : 'Запросить резервные коды' }}
        </button>
      </fieldset>
    </form>
  </section>
</template>
