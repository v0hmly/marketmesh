import { useAccount } from '../api/controller';

import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { onBeforeRouteLeave } from 'vue-router';

import { Code, ConnectError } from '@connectrpc/connect';

import type { Address, AddressBook, AddressInput } from '../api/types';

import { isAddressError, isProfilePending } from '../api/errors';

import { useSession } from '../../../shell/context';

import { GuardMismatchError, type SessionGuard } from '../../../shell/session';

import { accountError } from '../errors';

import {
  addressFields,
  copyAddress,
  emptyAddress,
  validateAddress,
  type AddressErrors,
} from './validation';
/** View-scoped state: CAS, owner changes, pending projection and private draft cleanup. */
export function useAddressBook() {
  const session = useSession();

  const account = useAccount();

  const book = shallowRef<AddressBook | null>(null);

  const latest = shallowRef<AddressBook | null>(null);

  const guard = shallowRef<SessionGuard | null>(null);

  const draft = ref<AddressInput>(emptyAddress());

  const initialDraft = ref<AddressInput>(emptyAddress());

  const editing = ref(false);

  const editingId = shallowRef<Uint8Array | null>(null);

  const deleting = shallowRef<Address | null>(null);

  /** Кнопка «Удалить», к которой диалог вернёт фокус. */
  const deleteTrigger = shallowRef<HTMLElement | null>(null);

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
      editing.value &&
      addressFields.some(({ key }) => draft.value[key] !== initialDraft.value[key]),
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
      if (session.state.value.status === 'profilePending') await account.readProfile();
      if (attempt !== revision || !permitted.value) return;
      const owner = guard.value ?? session.capture();
      const value = await account.readAddresses(owner);
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
    } else if (!address || editing.value || (kind === 'delete' && deleting.value !== address))
      return;
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
          ? await account.updateAddress({ ...input, addressId: editingId.value }, guard.value)
          : await account.createAddress(input, guard.value);
      } else {
        const input = { addressId: address!.addressId, expectedBookVersion };
        value =
          kind === 'delete'
            ? await account.deleteAddress(input, guard.value)
            : await account.setDefaultAddress(input, guard.value);
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
      // Диалог закрывается, чтобы сообщение об ошибке не осталось за подложкой.
      if (kind === 'delete') deleting.value = null;
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

  function requestDelete(address: Address, event?: Event) {
    if (busy.value || editing.value || reconcile.value || !permitted.value) return;
    deleting.value = address;
    deleteTrigger.value = event?.currentTarget instanceof HTMLElement ? event.currentTarget : null;
    failure.value = '';
    feedback.value = '';
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
  return {
    session,
    account,
    book,
    latest,
    draft,
    editing,
    editingId,
    deleting,
    deleteTrigger,
    loading,
    saving,
    reconcile,
    unknownCreate,
    failure,
    feedback,
    pending,
    errors,
    attempts,
    delays,
    permitted,
    busy,
    dirty,
    shown,
    idKey,
    editableLatest,
    read,
    retry,
    start,
    cancel,
    mutate,
    acceptLatest,
    keepDraft,
    requestDelete,
    addressFields,
  };
}
