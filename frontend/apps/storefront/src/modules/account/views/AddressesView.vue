<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { onBeforeRouteLeave } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Address, AddressBook, AddressInput } from '../../../shared/api/types';
import { isAddressError, isProfilePending } from '../../../shared/api/errors';
import { useSession } from '../../../shell/context';
import { GuardMismatchError, type SessionGuard } from '../../../shell/session';
import { accountError } from '../errors';
import {
  addressFields,
  copyAddress,
  emptyAddress,
  validateAddress,
  type AddressErrors,
} from '../address-validation';
import AccountNav from '../components/AccountNav.vue';

const session = useSession();
const book = shallowRef<AddressBook | null>(null);
const latest = shallowRef<AddressBook | null>(null);
const guard = shallowRef<SessionGuard | null>(null);
const draft = ref<AddressInput>(emptyAddress());
const initialDraft = ref<AddressInput>(emptyAddress());
const editing = ref(false);
const editingId = shallowRef<Uint8Array | null>(null);
const deleting = shallowRef<Address | null>(null);
const loading = ref(false);
const saving = ref(false);
const reconcile = ref(false);
const unknownCreate = ref(false);
const failure = ref('');
const feedback = ref('');
const pending = ref(false);
const errors = ref<AddressErrors>({});
const attempts = ref(0);
const delays = [1500, 3000, 6000, 12000];
let timer: ReturnType<typeof setTimeout> | undefined;
let revision = 0;
let active = true;
const permitted = computed(() => session.state.value.status === 'authenticated');
const busy = computed(() => loading.value || saving.value);
const dirty = computed(
  () =>
    editing.value && addressFields.some(({ key }) => draft.value[key] !== initialDraft.value[key]),
);
const shown = computed(() => latest.value ?? book.value);
const idKey = (id: Uint8Array) => Array.from(id).join('-');
const sameId = (a: Uint8Array, b: Uint8Array) => idKey(a) === idKey(b);
const editableLatest = computed(
  () =>
    !editingId.value ||
    latest.value?.addresses.some((address) => sameId(address.addressId, editingId.value!)),
);

function stopTimer() {
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
}
function clear() {
  revision++;
  stopTimer();
  attempts.value = 0;
  book.value = null;
  latest.value = null;
  guard.value = null;
  draft.value = emptyAddress();
  initialDraft.value = emptyAddress();
  editing.value = false;
  editingId.value = null;
  deleting.value = null;
  failure.value = '';
  feedback.value = '';
  errors.value = {};
  reconcile.value = false;
  unknownCreate.value = false;
  pending.value = false;
  loading.value = false;
  saving.value = false;
}
function schedule() {
  stopTimer();
  if (
    !active ||
    !(pending.value || session.state.value.status === 'profilePending') ||
    attempts.value >= delays.length
  )
    return;
  timer = setTimeout(() => {
    timer = undefined;
    attempts.value++;
    void read();
  }, delays[attempts.value]);
}
function resetEditor() {
  draft.value = emptyAddress();
  initialDraft.value = emptyAddress();
  editing.value = false;
  editingId.value = null;
  deleting.value = null;
  errors.value = {};
}
function accept(value: AddressBook) {
  book.value = value;
  guard.value = session.capture();
  latest.value = null;
  reconcile.value = false;
  unknownCreate.value = false;
  resetEditor();
}
async function recover() {
  try {
    await session.bootstrap();
  } catch {
    /* Shell owns session recovery. */
  }
}
async function read(compare = false) {
  if (busy.value || !active) return;
  if (!permitted.value && session.state.value.status !== 'profilePending') return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    if (session.state.value.status === 'profilePending') await session.readProfile();
    if (attempt !== revision || !permitted.value) return;
    const owner = guard.value ?? session.capture();
    const value = await session.readAddresses(owner);
    if (attempt !== revision || !active) return;
    pending.value = false;
    stopTimer();
    if (compare && book.value) {
      latest.value = value;
      reconcile.value = true;
      feedback.value =
        'Актуальная книга загружена. Проверьте список и выберите дальнейшее действие.';
    } else accept(value);
  } catch (error) {
    if (attempt !== revision) return;
    if (error instanceof GuardMismatchError) {
      clear();
      await recover();
      return;
    }
    pending.value = isProfilePending(error) || session.state.value.status === 'profilePending';
    if (!pending.value) failure.value = accountError(error);
  } finally {
    if (attempt === revision) {
      loading.value = false;
      schedule();
    }
  }
}
async function retry() {
  attempts.value = 0;
  stopTimer();
  await read(reconcile.value);
}
async function focusEditor() {
  await nextTick();
  document.getElementById('address-recipient')?.focus();
}
function start(address?: Address) {
  if (
    !permitted.value ||
    busy.value ||
    reconcile.value ||
    editing.value ||
    deleting.value ||
    !book.value ||
    (!address && book.value.addresses.length >= 20)
  )
    return;
  editingId.value = address?.addressId ?? null;
  draft.value = address?.fields ? copyAddress(address.fields) : emptyAddress();
  initialDraft.value = copyAddress(draft.value);
  editing.value = true;
  errors.value = {};
  failure.value = '';
  feedback.value = '';
  void focusEditor();
}
async function cancel() {
  if (busy.value || reconcile.value) return;
  if (dirty.value && !window.confirm('Удалить несохранённый черновик адреса?')) return;
  resetEditor();
  await nextTick();
  document.getElementById('add-address')?.focus();
}
async function mutate(kind: 'save' | 'delete' | 'default', address?: Address) {
  if (!permitted.value || !book.value || !guard.value || busy.value || reconcile.value) return;
  if (kind === 'save') {
    if (!editing.value) return;
    errors.value = validateAddress(draft.value);
    if (Object.keys(errors.value).length) {
      await nextTick();
      document.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
      return;
    }
  } else if (!address || editing.value || (kind === 'delete' && deleting.value !== address)) return;
  const attempt = revision;
  const creating = kind === 'save' && !editingId.value;
  saving.value = true;
  failure.value = '';
  feedback.value = '';
  const expectedBookVersion = book.value.version;
  try {
    let value: AddressBook;
    if (kind === 'save') {
      const input = { fields: copyAddress(draft.value), expectedBookVersion };
      value = editingId.value
        ? await session.updateAddress({ ...input, addressId: editingId.value }, guard.value)
        : await session.createAddress(input, guard.value);
    } else {
      const input = { addressId: address!.addressId, expectedBookVersion };
      value =
        kind === 'delete'
          ? await session.deleteAddress(input, guard.value)
          : await session.setDefaultAddress(input, guard.value);
    }
    if (attempt !== revision) return;
    accept(value);
    feedback.value =
      kind === 'delete'
        ? 'Адрес удалён.'
        : kind === 'default'
          ? 'Основной адрес выбран.'
          : 'Адрес сохранён.';
    await nextTick();
    document.getElementById('addresses-result')?.focus();
  } catch (error) {
    if (attempt !== revision) return;
    if (error instanceof GuardMismatchError) {
      clear();
      await recover();
      return;
    }
    if (error instanceof ConnectError && error.code === Code.InvalidArgument)
      failure.value = 'Проверьте поля адреса. Сервер не принял введённые данные.';
    else if (error instanceof ConnectError && error.code === Code.PermissionDenied)
      failure.value = accountError(error);
    else {
      reconcile.value = true;
      if (isAddressError(error, 'ADDRESS_NOT_FOUND'))
        failure.value = 'Адрес больше недоступен. Перечитайте книгу; черновик сохранён.';
      else if (isAddressError(error, 'ADDRESS_LIMIT_REACHED'))
        failure.value = 'В книге уже 20 адресов. Перечитайте список, прежде чем продолжать.';
      else if (error instanceof ConnectError && error.code === Code.Aborted)
        failure.value =
          'Книга изменена в другом окне. Черновик сохранён. Перечитайте актуальные данные.';
      else {
        unknownCreate.value = creating;
        failure.value =
          'Результат изменения не подтверждён. Автоматического повтора не будет. Перечитайте книгу перед следующим действием.';
        if (error instanceof ConnectError && error.code === Code.Unauthenticated) await recover();
      }
    }
  } finally {
    if (attempt === revision) saving.value = false;
  }
}
function acceptLatest() {
  if (!latest.value || busy.value || !permitted.value) return;
  accept(latest.value);
  failure.value = '';
  feedback.value = 'Показана актуальная адресная книга.';
}
function keepDraft() {
  if (
    !latest.value ||
    !editing.value ||
    !editableLatest.value ||
    busy.value ||
    !permitted.value ||
    (!editingId.value && latest.value.addresses.length >= 20)
  )
    return;
  book.value = latest.value;
  latest.value = null;
  guard.value = session.capture();
  reconcile.value = false;
  unknownCreate.value = false;
  failure.value = '';
  feedback.value =
    'Черновик сохранён. Проверьте поля и нажмите «Сохранить адрес», чтобы отправить его с актуальной версией книги.';
}
function requestDelete(address: Address) {
  if (busy.value || editing.value || reconcile.value || !permitted.value) return;
  deleting.value = address;
  failure.value = '';
  feedback.value = '';
  void nextTick(() => document.getElementById('cancel-delete')?.focus());
}
watch(() => session.state.value.generation, clear, { flush: 'sync' });
watch(
  () => [session.state.value.status, session.state.value.subjectId] as const,
  ([status, subjectId]) => {
    if (
      (guard.value && subjectId && subjectId !== guard.value.subjectId) ||
      ['anonymous', 'switching', 'signingOut', 'uncertain', 'unsupported'].includes(status)
    )
      clear();
  },
  { flush: 'sync' },
);
watch(
  () => [session.state.value.generation, session.state.value.subjectId, permitted.value] as const,
  () => {
    if (permitted.value && !book.value) void read();
  },
  { immediate: true },
);
watch(
  () => session.state.value.status,
  (status) => {
    if (status === 'profilePending') schedule();
    else if (!pending.value) stopTimer();
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
onBeforeRouteLeave(
  () =>
    !dirty.value ||
    window.confirm('Есть несохранённый адрес. Покинуть страницу и удалить черновик?'),
);
onBeforeUnmount(() => {
  active = false;
  window.removeEventListener('beforeunload', beforeUnload);
  clear();
});
</script>

<template>
  <section aria-labelledby="addresses-title">
    <AccountNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ЛИЧНЫЙ КАБИНЕТ</span>
        <h1 id="addresses-title">Адреса доставки</h1>
        <p class="lede">Сохраните удобные адреса, чтобы они были под рукой.</p>
      </div>
      <span class="section-number" aria-hidden="true">02 / АДРЕСА</span>
    </div>
    <div v-if="pending || session.state.value.status === 'profilePending'" class="card state-card">
      <h2>Готовим вашу адресную книгу</h2>
      <p role="status">Профиль создаётся. Данные появятся после подготовки аккаунта.</p>
      <p v-if="attempts >= delays.length">Автоматическая проверка приостановлена.</p>
      <button class="button secondary" :disabled="busy" @click="retry">Проверить готовность</button>
    </div>
    <div v-else-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы открыть свои адреса.</p>
        <RouterLink class="button primary" to="/login">Перейти ко входу</RouterLink></template
      >
    </div>
    <div v-else-if="!book" class="card state-card" :aria-busy="loading">
      <p v-if="loading" role="status">Загружаем адреса…</p>
      <template v-else
        ><h2>Адреса пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read()">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="address-content" :aria-busy="busy">
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
      <p v-if="feedback" id="addresses-result" class="notice success" role="status" tabindex="-1">
        {{ feedback }}
      </p>
      <div v-if="reconcile" class="reconcile-panel" aria-labelledby="address-reconcile-title">
        <h2 id="address-reconcile-title">Сверим адресную книгу</h2>
        <p v-if="unknownCreate">
          Новый адрес мог уже сохраниться. Проверьте список, чтобы не создать дубликат.
        </p>
        <button class="button secondary" :disabled="busy" @click="read(true)">
          Перечитать актуальные данные
        </button>
        <template v-if="latest">
          <p>Ниже показан актуальный список. Ваш черновик остаётся в форме.</p>
          <p v-if="editing && !editableLatest" role="status">
            Редактируемый адрес удалён. Этот черновик нельзя сохранить поверх другой записи.
          </p>
          <div class="button-row">
            <button class="button secondary" :disabled="busy" @click="acceptLatest">
              Принять актуальную книгу
            </button>
            <button
              v-if="editing && editableLatest && (editingId || latest.addresses.length < 20)"
              class="button text-button"
              :disabled="busy"
              @click="keepDraft"
            >
              {{
                unknownCreate
                  ? 'Проверил список: подготовить ещё один адрес'
                  : 'Оставить мой черновик для сохранения'
              }}
            </button>
          </div>
        </template>
      </div>
      <div class="address-toolbar">
        <p class="subtle">
          {{ shown?.addresses.length }} / 20 адресов. Контакты доступны только вам.
        </p>
        <button
          id="add-address"
          class="button primary"
          :disabled="
            busy || editing || Boolean(deleting) || reconcile || book.addresses.length >= 20
          "
          @click="start()"
        >
          Добавить адрес
        </button>
      </div>
      <p v-if="!shown?.addresses.length" class="card address-empty">
        Пока нет сохранённых адресов. Первый адрес станет основным.
      </p>
      <ul v-else class="address-list" aria-label="Сохранённые адреса">
        <li
          v-for="address in shown.addresses"
          :key="idKey(address.addressId)"
          class="card address-card"
        >
          <div class="card-heading">
            <h2>{{ address.fields?.recipient }}</h2>
            <span v-if="address.isDefault" class="draft-badge">По умолчанию</span>
          </div>
          <p>{{ address.fields?.phone }}</p>
          <p>
            {{
              [
                address.fields?.country,
                address.fields?.postalCode,
                address.fields?.city,
                address.fields?.streetHouse,
                address.fields?.apartment,
              ]
                .filter(Boolean)
                .join(', ')
            }}
          </p>
          <p v-if="address.fields?.comment" class="plain-text">{{ address.fields.comment }}</p>
          <div class="button-row">
            <button
              class="button secondary"
              :disabled="busy || editing || Boolean(deleting) || reconcile"
              @click="start(address)"
            >
              Изменить<span class="visually-hidden">
                адрес {{ address.fields?.recipient }}</span
              ></button
            ><button
              v-if="!address.isDefault"
              class="button text-button"
              :disabled="busy || editing || Boolean(deleting) || reconcile"
              @click="mutate('default', address)"
            >
              Использовать по умолчанию<span class="visually-hidden"
                >: {{ address.fields?.recipient }}</span
              ></button
            ><button
              class="button text-button"
              :disabled="busy || editing || Boolean(deleting) || reconcile"
              @click="requestDelete(address)"
            >
              Удалить<span class="visually-hidden"> адрес {{ address.fields?.recipient }}</span>
            </button>
          </div>
        </li>
      </ul>
      <div
        v-if="deleting && !reconcile"
        class="reconcile-panel"
        aria-labelledby="delete-address-title"
      >
        <h2 id="delete-address-title">Удалить адрес?</h2>
        <p>
          {{ deleting.fields?.recipient }} — {{ deleting.fields?.city }},
          {{ deleting.fields?.streetHouse }}
        </p>
        <p v-if="deleting.isDefault">Другой основной адрес не будет выбран автоматически.</p>
        <div class="button-row">
          <button
            id="cancel-delete"
            class="button secondary"
            :disabled="busy"
            @click="deleting = null"
          >
            Отменить удаление</button
          ><button class="button primary" :disabled="busy" @click="mutate('delete', deleting)">
            Подтвердить удаление
          </button>
        </div>
      </div>
      <div v-if="editing" class="card profile-card address-editor">
        <h2>{{ editingId ? 'Изменить адрес' : 'Новый адрес' }}</h2>
        <p class="subtle">
          Обязательные поля отмечены *. Телефон нужен для получения; он не меняет логин.
        </p>
        <form novalidate :aria-busy="saving" @submit.prevent="mutate('save')">
          <div v-for="field in addressFields" :key="field.key" class="field">
            <label :for="`address-${field.key}`"
              >{{ field.label }}<span v-if="field.required" aria-hidden="true"> *</span></label
            >
            <textarea
              v-if="field.key === 'comment'"
              :id="`address-${field.key}`"
              v-model="draft[field.key]"
              rows="3"
              :disabled="busy"
              :aria-invalid="Boolean(errors[field.key])"
              :aria-describedby="`address-${field.key}-help${errors[field.key] ? ` address-${field.key}-error` : ''}`"
            ></textarea>
            <input
              v-else
              :id="`address-${field.key}`"
              v-model="draft[field.key]"
              :type="field.key === 'phone' ? 'tel' : 'text'"
              :autocomplete="field.autocomplete"
              :required="field.required"
              :disabled="busy"
              :aria-invalid="Boolean(errors[field.key])"
              :aria-describedby="`address-${field.key}-help${errors[field.key] ? ` address-${field.key}-error` : ''}`"
            />
            <span :id="`address-${field.key}-help`" class="field-help"
              >До {{ field.limit }} символов{{ field.required ? '' : ', необязательно' }}.</span
            >
            <span v-if="errors[field.key]" :id="`address-${field.key}-error`" class="field-error">{{
              errors[field.key]
            }}</span>
          </div>
          <div class="form-footer">
            <span class="field-help">{{
              dirty ? 'Есть несохранённые изменения' : 'Проверьте адрес перед сохранением'
            }}</span>
            <div class="button-row">
              <button
                class="button secondary"
                type="button"
                :disabled="busy || reconcile"
                @click="cancel"
              >
                Отменить</button
              ><button class="button primary" type="submit" :disabled="busy || reconcile">
                {{ saving ? 'Сохраняем…' : 'Сохранить адрес' }}
              </button>
            </div>
          </div>
        </form>
      </div>
    </div>
  </section>
</template>
