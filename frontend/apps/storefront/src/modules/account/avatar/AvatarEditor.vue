<script setup lang="ts">
import { computed, inject, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { useSession } from '../../../shell/context';
import { GuardMismatchError, type SessionGuard } from '../../../shell/session';
import {
  avatarApiKey,
  createAvatarApi,
  FileState,
  idText,
  prepareUpload,
  type Avatar,
  type PreparedUpload,
} from './api';

const props = defineProps<{ initials: string }>();
const emit = defineEmits<{ image: [url: string] }>();
const api = inject(avatarApiKey, null) ?? createAvatarApi();
const session = useSession();
const current = shallowRef<Avatar | null>(null);
const selected = shallowRef<File | null>(null);
const prepared = shallowRef<PreparedUpload | null>(null);
const candidate = shallowRef<Uint8Array | null>(null);
const imageURL = ref('');
const busy = ref(false);
const failure = ref('');
const feedback = ref('');
const candidateReady = ref(false);
const uncertain = ref(false);
const processing = ref(false);
const stage = ref('');
const field = ref<HTMLInputElement | null>(null);
let controller = new AbortController();
let revision = 0;
let active = true;
let recovering: SessionGuard | null = null;
const canUpload = computed(
  () => !!current.value && !!selected.value && !busy.value && !uncertain.value && !processing.value,
);
function replaceImage(url: string) {
  if (imageURL.value) URL.revokeObjectURL(imageURL.value);
  imageURL.value = url;
  emit('image', url);
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
  current.value = null;
  selected.value = null;
  prepared.value = null;
  candidate.value = null;
  candidateReady.value = false;
  failure.value = '';
  feedback.value = '';
  busy.value = false;
  uncertain.value = false;
  processing.value = false;
  stage.value = '';
  if (field.value) field.value.value = '';
}
function call<T>(guard: SessionGuard, action: () => Promise<T>) {
  return session.withSession(guard, action);
}
async function readCall<T>(guard: SessionGuard, token: number, action: () => Promise<T>) {
  try {
    return await call(guard, action);
  } catch (error) {
    if (!(error instanceof ConnectError) || error.code !== Code.Unauthenticated) throw error;
    // withSession has released its shared lock. Only reads may recover/retry.
    recovering = guard;
    try {
      await session.bootstrap();
    } finally {
      recovering = null;
    }
    if (!valid(guard, token)) throw new GuardMismatchError();
    return call(guard, action);
  }
}
function resetCandidate() {
  candidate.value = null;
  candidateReady.value = false;
  selected.value = null;
  prepared.value = null;
  processing.value = false;
  if (field.value) field.value.value = '';
}
function describe(error: unknown) {
  if (error instanceof ConnectError && error.code === Code.InvalidArgument)
    return 'Файл не подходит. Выберите PNG или JPEG размером до 5 МиБ.';
  if (error instanceof ConnectError && error.code === Code.FailedPrecondition)
    return 'Файл пока недоступен или уже удалён. Проверьте обработку либо выберите другой.';
  if (error instanceof ConnectError && error.code === Code.Aborted)
    return 'Аватар изменён в другой вкладке. Перечитайте сохранённое состояние перед новой попыткой.';
  return 'Результат запроса не подтверждён. Перечитайте состояние перед новой попыткой.';
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
      failure.value = 'Аватар сохранён, но изображение пока недоступно. Обновите состояние позже.';
  }
}
async function read() {
  const guard = session.capture();
  const token = revision;
  busy.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const a = await readCall(guard, token, () => api.get(guard.subjectId ?? '', controller.signal));
    if (!valid(guard, token)) return;
    current.value = a;
    uncertain.value = false;
    if (candidate.value && idText(candidate.value) === idText(a.fileId)) {
      resetCandidate();
      feedback.value = 'Аватар сохранён.';
    }
    await showImage(guard, token);
    return valid(guard, token);
  } catch (error) {
    if (valid(guard, token)) failure.value = describe(error);
  } finally {
    if (valid(guard, token)) busy.value = false;
  }
}
function select(event: Event) {
  selected.value = (event.target as HTMLInputElement).files?.[0] ?? null;
  prepared.value = null;
  failure.value = '';
  feedback.value = '';
}
async function attach(guard: SessionGuard, token: number) {
  const id = candidate.value;
  const version = current.value?.version;
  if (!id || !version) return;
  uncertain.value = true;
  stage.value = 'Сохраняем аватар…';
  const a = await call(guard, () => api.set(id, version, guard.subjectId ?? '', controller.signal));
  if (!valid(guard, token)) return;
  current.value = a;
  resetCandidate();
  uncertain.value = false;
  feedback.value = 'Аватар сохранён.';
  if (field.value) field.value.value = '';
  await showImage(guard, token);
}
async function poll(guard: SessionGuard, token: number, attachWhenReady: boolean) {
  const id = candidate.value;
  if (!id) return;
  processing.value = true;
  candidateReady.value = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    const r = await readCall(guard, token, () => api.status(id, controller.signal));
    if (!valid(guard, token)) return;
    if (r.state === FileState.READY) {
      processing.value = false;
      candidateReady.value = true;
      if (attachWhenReady) await attach(guard, token);
      else feedback.value = 'Проверка завершена. Можно сохранить выбранный аватар.';
      return;
    }
    if ([FileState.REJECTED, FileState.EXPIRED, FileState.DELETED].includes(r.state)) {
      processing.value = false;
      throw new ConnectError('File rejected', Code.FailedPrecondition);
    }
    if (r.state === FileState.UPLOADING) {
      processing.value = false;
      feedback.value = 'Загрузка не завершена. Можно продолжить её с тем же файлом.';
      return;
    }
    stage.value = 'Проверяем и обрабатываем изображение…';
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
  feedback.value = 'Файл ещё обрабатывается. Проверьте готовность позже.';
}
async function upload() {
  if (!canUpload.value || !selected.value) return;
  const guard = session.capture();
  const token = revision;
  busy.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    stage.value = 'Готовим загрузку…';
    const p = prepared.value ?? (await prepareUpload(selected.value));
    if (!valid(guard, token)) return;
    prepared.value = p;
    const r = await call(guard, () => api.create(p, controller.signal));
    if (!valid(guard, token)) return;
    if (candidate.value && idText(candidate.value) !== idText(r.fileId))
      throw new ConnectError('Manifest changed', Code.DataLoss);
    candidate.value = r.fileId;
    candidateReady.value = false;
    if (r.state === FileState.UPLOADING) {
      stage.value = 'Загружаем изображение…';
      for (const part of r.parts) await call(guard, () => api.put(part, p.file, controller.signal));
      await call(guard, () => api.complete(r.fileId, controller.signal));
    }
    await poll(guard, token, true);
  } catch (error) {
    if (valid(guard, token)) {
      failure.value = describe(error);
      if (candidate.value) uncertain.value = true;
    }
  } finally {
    if (valid(guard, token)) {
      busy.value = false;
      stage.value = '';
    }
  }
}
async function checkCandidate() {
  if (!(await read())) return;
  if (uncertain.value || !candidate.value || busy.value) return;
  const guard = session.capture();
  const token = revision;
  busy.value = true;
  try {
    await poll(guard, token, false);
  } catch (error) {
    if (valid(guard, token)) failure.value = describe(error);
  } finally {
    if (valid(guard, token)) {
      busy.value = false;
      stage.value = '';
    }
  }
}
async function saveCandidate() {
  const guard = session.capture();
  const token = revision;
  busy.value = true;
  failure.value = '';
  try {
    await attach(guard, token);
  } catch (error) {
    if (valid(guard, token)) failure.value = describe(error);
  } finally {
    if (valid(guard, token)) {
      busy.value = false;
      stage.value = '';
    }
  }
}
async function remove() {
  if (!current.value || busy.value || uncertain.value) return;
  const guard = session.capture();
  const token = revision;
  const version = current.value.version;
  busy.value = true;
  uncertain.value = true;
  failure.value = '';
  try {
    const a = await call(guard, () => api.clear(version, guard.subjectId ?? '', controller.signal));
    if (!valid(guard, token)) return;
    current.value = a;
    replaceImage('');
    uncertain.value = false;
    feedback.value = 'Аватар удалён. Очистка файла выполняется в фоне.';
  } catch (error) {
    if (valid(guard, token)) failure.value = describe(error);
  } finally {
    if (valid(guard, token)) busy.value = false;
  }
}
async function cancelCandidate() {
  if (!candidate.value || uncertain.value || busy.value) return;
  const guard = session.capture();
  const token = revision;
  const id = candidate.value;
  busy.value = true;
  try {
    await call(guard, () => api.remove(id, controller.signal));
    if (!valid(guard, token)) return;
    resetCandidate();
    feedback.value = 'Загрузка отменена.';
  } catch (error) {
    if (valid(guard, token)) {
      uncertain.value = true;
      failure.value = describe(error);
    }
  } finally {
    if (valid(guard, token)) busy.value = false;
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
  <section class="card profile-card" aria-labelledby="avatar-title" :aria-busy="busy">
    <h2 id="avatar-title">Ваш аватар.</h2>
    <div class="initials">
      <img v-if="imageURL" :src="imageURL" alt="Ваш сохранённый аватар" /><span
        v-else
        aria-hidden="true"
        >{{ props.initials }}</span
      >
    </div>
    <p class="field-help">
      PNG или JPEG до 5 МиБ. Изображение появится после проверки и обработки.
    </p>
    <p v-if="stage" role="status">{{ stage }}</p>
    <p v-if="failure" id="avatar-error" class="notice error" role="alert">{{ failure }}</p>
    <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
    <form novalidate @submit.prevent="upload">
      <div class="field">
        <label for="avatar-file">Изображение для аватара</label
        ><input
          id="avatar-file"
          ref="field"
          type="file"
          accept="image/png,image/jpeg"
          :disabled="busy || !!candidate || uncertain"
          :aria-describedby="failure ? 'avatar-help avatar-error' : 'avatar-help'"
          :aria-invalid="!!failure"
          @change="select"
        />
        <p id="avatar-help" class="field-help">
          Исходный файл не показывается. Сохраняется проверенная производная.
        </p>
      </div>
      <div class="button-row">
        <!-- Вторичная: основное действие экрана MarketMesh ID — сохранить открытую форму. -->
        <button class="button secondary" type="submit" :disabled="!canUpload">
          {{ busy ? 'Обрабатываем…' : candidate ? 'Продолжить загрузку' : 'Загрузить аватар' }}
        </button>
        <button class="button secondary" type="button" :disabled="busy" @click="checkCandidate">
          Обновить состояние
        </button>
        <button
          v-if="candidate && candidateReady && !processing && !uncertain"
          class="button secondary"
          type="button"
          :disabled="busy"
          @click="saveCandidate"
        >
          Сохранить выбранный аватар
        </button>
        <button
          v-if="candidate"
          class="button text-button"
          type="button"
          :disabled="busy || uncertain"
          @click="cancelCandidate"
        >
          Отменить загрузку
        </button>
        <button
          v-if="current?.fileId.length"
          class="button text-button"
          type="button"
          :disabled="busy || uncertain || !!candidate"
          @click="remove"
        >
          Удалить аватар
        </button>
      </div>
    </form>
    <p v-if="uncertain" class="reconcile-panel" role="status">
      Запись могла выполниться. Нажмите «Обновить состояние», чтобы сверить сохранённый аватар.
    </p>
  </section>
</template>
