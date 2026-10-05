<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import ConfirmDialog from '@marketmesh/design-system/ConfirmDialog.vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { useSession } from '../../../shell/context';
import { GuardMismatchError, type SessionGuard } from '../../../shell/session';
import {
  avatarApiKey,
  createAvatarApi,
  FileState,
  idText,
  MAX_SOURCE,
  prepareUpload,
  type Avatar,
} from './api';

/**
 * Фото профиля в шапке MarketMesh ID: круг и действия под именем. Покупатель видит только
 * «Сохраняем фото…», «Фото сохранено» и понятный отказ; загрузку, проверку в Files и запись
 * с CAS компонент проводит сам. Неизвестный исход записи он сначала перечитывает сам и
 * спрашивает человека, только если связь так и не вернулась.
 */
const props = defineProps<{ initials: string }>();
const api = inject(avatarApiKey, null) ?? createAvatarApi();
const session = useSession();
const current = shallowRef<Avatar | null>(null);
const imageURL = ref('');
/** Выбранный файл и его локальное превью; пока он есть, открыт диалог подтверждения. */
const choice = shallowRef<{ file: File; url: string } | null>(null);
const asking = ref(false);
const saving = ref(false);
const removing = ref(false);
const reading = ref(false);
const stage = ref('');
const failure = ref('');
const feedback = ref('');
/** Исход, который не удалось перечитать: чтение, запись нового фото или удаление. */
const unsure = ref<'' | 'read' | 'save' | 'remove'>('');
const field = ref<HTMLInputElement | null>(null);
const trigger = ref<HTMLButtonElement | null>(null);
const remover = ref<HTMLButtonElement | null>(null);
let pending: { id: Uint8Array; version: bigint } | null = null;
let removal: bigint | null = null;
let controller = new AbortController();
let revision = 0;
let active = true;
let recovering: SessionGuard | null = null;

const unsuitable = 'Этот файл не подойдёт. Нужен PNG или JPEG размером до 5 МБ.';
const changedElsewhere = 'Фото уже изменили в другой вкладке. Показываем сохранённое.';
const unsureText = {
  read: 'Не удалось получить фото профиля. Изменить его можно после проверки.',
  save: 'Связь прервалась, и мы не знаем, сохранилось ли новое фото.',
  remove: 'Связь прервалась, и мы не знаем, удалилось ли фото.',
};
const hasPhoto = computed(() => Boolean(current.value?.fileId.length));
const busy = computed(() => saving.value || removing.value || reading.value);
const canChange = computed(() => !busy.value && !unsure.value && Boolean(current.value));

function replaceImage(url: string) {
  if (imageURL.value) URL.revokeObjectURL(imageURL.value);
  imageURL.value = url;
}
function dropChoice() {
  if (choice.value) URL.revokeObjectURL(choice.value.url);
  choice.value = null;
}
function valid(guard: SessionGuard, token: number) {
  const s = session.state.value;
  return (
    active &&
    revision === token &&
    s.generation === guard.generation &&
    s.subjectId === guard.subjectId &&
    s.status === 'authenticated'
  );
}
function clearState() {
  revision++;
  controller.abort();
  controller = new AbortController();
  replaceImage('');
  dropChoice();
  current.value = null;
  pending = null;
  removal = null;
  asking.value = false;
  saving.value = false;
  removing.value = false;
  reading.value = false;
  stage.value = '';
  failure.value = '';
  feedback.value = '';
  unsure.value = '';
}
function call<T>(guard: SessionGuard, action: () => Promise<T>) {
  return session.withSession(guard, action);
}
const expired = (error: unknown) =>
  error instanceof ConnectError && error.code === Code.Unauthenticated;
/** Обновляет сессию того же владельца; смена владельца прерывает действие. */
async function recover(guard: SessionGuard, token: number) {
  recovering = guard;
  try {
    await session.bootstrap();
  } finally {
    recovering = null;
  }
  if (!valid(guard, token)) throw new GuardMismatchError();
}
async function readCall<T>(guard: SessionGuard, token: number, action: () => Promise<T>) {
  try {
    return await call(guard, action);
  } catch (error) {
    if (!expired(error)) throw error;
    // withSession has released its shared lock. Only reads may recover/retry.
    await recover(guard, token);
    return call(guard, action);
  }
}
/** Непривязанный файл не становится фото профиля; ошибка удаления покупателю не важна. */
function discard(guard: SessionGuard, id: Uint8Array) {
  void call(guard, () => api.remove(id, controller.signal)).catch(() => {});
}
async function showImage(guard: SessionGuard, token: number) {
  replaceImage('');
  const id = current.value?.fileId;
  if (!id?.length) return;
  try {
    const blob = await readCall(guard, token, () => api.image(id, controller.signal));
    if (valid(guard, token)) replaceImage(URL.createObjectURL(blob));
  } catch {
    if (valid(guard, token))
      failure.value =
        'Фото сохранено, но показать его сейчас не получается. Обновите страницу позже.';
  }
}
async function read() {
  const guard = session.capture();
  const token = revision;
  reading.value = true;
  try {
    const saved = await readCall(guard, token, () =>
      api.get(guard.subjectId ?? '', controller.signal),
    );
    if (!valid(guard, token)) return;
    current.value = saved;
    unsure.value = '';
    await showImage(guard, token);
  } catch {
    if (valid(guard, token)) unsure.value = 'read';
  } finally {
    if (valid(guard, token)) reading.value = false;
  }
}

function choose() {
  if (!canChange.value) return;
  field.value?.click();
}
function select(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0] ?? null;
  // Тот же файл можно выбрать снова, например после отмены диалога.
  input.value = '';
  if (!file || !canChange.value) return;
  failure.value = '';
  feedback.value = '';
  if (!['image/png', 'image/jpeg'].includes(file.type) || file.size < 1 || file.size > MAX_SOURCE) {
    failure.value = unsuitable;
    return;
  }
  choice.value = { file, url: URL.createObjectURL(file) };
}
function confirmChoice() {
  const file = choice.value?.file;
  dropChoice();
  if (file) void upload(file);
}

/** Ждёт окончательного состояния файла; `null` — проверка не уложилась в две минуты. */
async function settle(guard: SessionGuard, token: number, id: Uint8Array) {
  const final = [
    FileState.READY,
    FileState.REJECTED,
    FileState.EXPIRED,
    FileState.DELETED,
    FileState.UPLOADING,
  ];
  for (let attempt = 0; attempt < 60; attempt++) {
    const r = await readCall(guard, token, () => api.status(id, controller.signal));
    if (!valid(guard, token)) throw new GuardMismatchError();
    if (final.includes(r.state)) return r.state;
    await new Promise<void>((resolve, reject) => {
      const signal = controller.signal;
      const abort = () => {
        clearTimeout(timer);
        reject(new GuardMismatchError());
      };
      const timer = setTimeout(() => {
        signal.removeEventListener('abort', abort);
        resolve();
      }, 2000);
      signal.addEventListener('abort', abort, { once: true });
    });
  }
  return null;
}
function rejection(state: FileState | null) {
  if (state === FileState.REJECTED)
    return 'Фото не прошло проверку. Мы его не сохранили. Выберите другое фото.';
  if (state === null)
    return 'Проверка фото затянулась. Мы его не сохранили. Попробуйте ещё раз позже.';
  return 'Загрузка фото прервалась. Мы его не сохранили. Попробуйте ещё раз.';
}
async function upload(file: File) {
  const guard = session.capture();
  const token = revision;
  saving.value = true;
  failure.value = '';
  feedback.value = '';
  stage.value = 'Сохраняем фото…';
  let id: Uint8Array | null = null;
  try {
    const prepared = await prepareUpload(file);
    if (!valid(guard, token)) return;
    const send = async () => {
      const created = await call(guard, () => api.create(prepared, controller.signal));
      if (!valid(guard, token)) throw new GuardMismatchError();
      id = created.fileId;
      if (created.state === FileState.UPLOADING) {
        for (const part of created.parts)
          await call(guard, () => api.put(part, prepared.file, controller.signal));
        await call(guard, () => api.complete(created.fileId, controller.signal));
      }
      return created;
    };
    // Истёкший доступ отклоняется до записи; ключ идемпотентности тот же, файл не задвоится.
    const created = await send().catch(async (error: unknown) => {
      if (!expired(error)) throw error;
      await recover(guard, token);
      return send();
    });
    stage.value = 'Проверяем фото…';
    const state = await settle(guard, token, created.fileId);
    if (!valid(guard, token)) return;
    if (state !== FileState.READY) {
      discard(guard, created.fileId);
      failure.value = rejection(state);
      return;
    }
    const version = current.value?.version;
    if (!version) throw new GuardMismatchError();
    stage.value = 'Сохраняем фото…';
    pending = { id: created.fileId, version };
    await attach(guard, token, true);
  } catch (error) {
    if (!valid(guard, token) || pending) return;
    if (id) discard(guard, id);
    failure.value =
      error instanceof ConnectError && error.code === Code.InvalidArgument
        ? unsuitable
        : 'Не удалось загрузить фото. Мы ничего не изменили. Попробуйте ещё раз.';
  } finally {
    if (valid(guard, token)) {
      saving.value = false;
      stage.value = '';
    }
  }
}
/** Записывает проверенный файл; при любой ошибке исход сверяется чтением, а не повтором вслепую. */
async function attach(guard: SessionGuard, token: number, retry: boolean) {
  const target = pending;
  if (!target) return;
  try {
    const saved = await call(guard, () =>
      api.set(target.id, target.version, guard.subjectId ?? '', controller.signal),
    );
    if (!valid(guard, token)) return;
    pending = null;
    current.value = saved;
    feedback.value = 'Фото сохранено.';
    await showImage(guard, token);
  } catch {
    if (valid(guard, token)) await reconcileSave(guard, token, retry);
  }
}
/** Повтор записи безопасен только при прежней версии: CAS не даст записать её дважды. */
async function reconcileSave(guard: SessionGuard, token: number, retry: boolean) {
  const target = pending;
  if (!target) return;
  let saved: Avatar;
  try {
    saved = await readCall(guard, token, () => api.get(guard.subjectId ?? '', controller.signal));
  } catch {
    if (valid(guard, token)) unsure.value = 'save';
    return;
  }
  if (!valid(guard, token)) return;
  current.value = saved;
  unsure.value = '';
  if (idText(saved.fileId) === idText(target.id)) {
    pending = null;
    feedback.value = 'Фото сохранено.';
  } else if (saved.version === target.version && retry) {
    return attach(guard, token, false);
  } else {
    pending = null;
    discard(guard, target.id);
    failure.value =
      saved.version === target.version
        ? 'Не удалось сохранить фото. Мы ничего не изменили. Попробуйте ещё раз.'
        : changedElsewhere;
  }
  await showImage(guard, token);
}

function askRemove() {
  if (!canChange.value || !hasPhoto.value) return;
  failure.value = '';
  feedback.value = '';
  asking.value = true;
}
async function remove() {
  const saved = current.value;
  if (!saved || removing.value) return;
  const guard = session.capture();
  const token = revision;
  removing.value = true;
  try {
    const cleared = await call(guard, () =>
      api.clear(saved.version, guard.subjectId ?? '', controller.signal),
    );
    if (!valid(guard, token)) return;
    current.value = cleared;
    replaceImage('');
    feedback.value = 'Фото удалено. Вместо него показываем инициалы.';
  } catch {
    if (!valid(guard, token)) return;
    removal = saved.version;
    await reconcileRemove(guard, token);
  } finally {
    if (valid(guard, token)) {
      removing.value = false;
      asking.value = false;
      // «Удалить фото» исчезает вместе с фото: фокус остаётся у соседнего действия.
      await nextTick();
      if (!hasPhoto.value) trigger.value?.focus();
    }
  }
}
async function reconcileRemove(guard: SessionGuard, token: number) {
  const version = removal;
  if (version === null) return;
  let saved: Avatar;
  try {
    saved = await readCall(guard, token, () => api.get(guard.subjectId ?? '', controller.signal));
  } catch {
    if (valid(guard, token)) unsure.value = 'remove';
    return;
  }
  if (!valid(guard, token)) return;
  removal = null;
  unsure.value = '';
  current.value = saved;
  if (!saved.fileId.length) {
    replaceImage('');
    feedback.value = 'Фото удалено. Вместо него показываем инициалы.';
    return;
  }
  failure.value =
    saved.version === version
      ? 'Не удалось удалить фото. Оно осталось прежним. Попробуйте ещё раз.'
      : changedElsewhere;
  await showImage(guard, token);
}
async function recheck() {
  const kind = unsure.value;
  if (!kind || reading.value) return;
  if (kind === 'read') return read();
  const guard = session.capture();
  const token = revision;
  reading.value = true;
  try {
    if (kind === 'save') await reconcileSave(guard, token, true);
    else await reconcileRemove(guard, token);
  } finally {
    if (valid(guard, token)) reading.value = false;
  }
}

watch(
  () => [session.state.value.generation, session.state.value.subjectId, session.state.value.status],
  () => {
    const state = session.state.value;
    if (
      recovering &&
      state.generation === recovering.generation &&
      state.subjectId === recovering.subjectId &&
      (state.status === 'checking' || state.status === 'authenticated')
    )
      return;
    clearState();
    if (session.state.value.status === 'authenticated') void read();
  },
  { immediate: true, flush: 'sync' },
);
onBeforeUnmount(() => {
  active = false;
  clearState();
});
</script>

<template>
  <!-- Круг дублирует «Изменить фото» для мыши и касания; с клавиатуры действует кнопка. -->
  <div
    class="initials avatar-photo"
    :class="{ 'avatar-photo-action': canChange }"
    :aria-busy="busy"
    @click="choose"
  >
    <img v-if="imageURL" :src="imageURL" alt="Ваше фото профиля" /><span
      v-else
      aria-hidden="true"
      >{{ props.initials }}</span
    >
  </div>
  <div class="avatar-actions">
    <button
      ref="trigger"
      type="button"
      class="button text-button"
      :aria-disabled="!canChange"
      @click="choose"
    >
      {{ hasPhoto ? 'Изменить фото' : 'Добавить фото' }}</button
    ><button
      v-if="hasPhoto"
      ref="remover"
      type="button"
      class="button text-button"
      :aria-disabled="!canChange"
      @click="askRemove"
    >
      Удалить фото
    </button>
  </div>
  <div class="avatar-feedback">
    <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
    <div class="avatar-status" role="status">
      <p v-if="stage" class="avatar-progress">
        <span class="loading-dot" aria-hidden="true"></span>{{ stage }}
      </p>
      <p v-else-if="feedback" class="notice success">{{ feedback }}</p>
      <div v-else-if="unsure" class="reconcile-panel avatar-recheck">
        <p>{{ unsureText[unsure] }}</p>
        <button class="button secondary" type="button" :disabled="reading" @click="recheck">
          {{ reading ? 'Проверяем…' : 'Проверить ещё раз' }}
        </button>
      </div>
    </div>
  </div>
  <input
    ref="field"
    type="file"
    accept="image/png,image/jpeg"
    hidden
    data-avatar-file
    @change="select"
  />
  <Teleport to="body">
    <ConfirmDialog
      v-if="choice"
      title="Новое фото профиля"
      confirm-label="Сохранить фото"
      cancel-label="Отменить"
      :return-focus="trigger"
      @confirm="confirmChoice"
      @cancel="dropChoice"
    >
      <div class="initials avatar-preview">
        <img :src="choice.url" alt="Выбранное фото" />
      </div>
      <p>Так фото будет выглядеть в круге. Перед сохранением мы проверим файл.</p>
    </ConfirmDialog>
    <ConfirmDialog
      v-if="asking"
      title="Удалить фото профиля?"
      confirm-label="Удалить фото"
      cancel-label="Оставить фото"
      :busy="removing"
      :return-focus="remover"
      @confirm="remove"
      @cancel="asking = false"
    >
      <p>Вместо фото мы покажем ваши инициалы.</p>
    </ConfirmDialog>
  </Teleport>
</template>
