import { accountKey } from '../api/controller';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import IdView from '../profile/IdView.vue';
import { Code, ConnectError } from '@connectrpc/connect';
import type { SessionController, SessionState } from '../../../testing/session';
import { sessionKey } from '../../../shell/context';
import { avatarApiKey, FileState, type AvatarApi, type Avatar } from './api';
import AvatarEditor from './AvatarEditor.vue';

vi.mock('../../../shared/features', async (original) => ({
  ...(await original<object>()),
  avatarEnabled: true,
}));
vi.mock('./api', async (original) => ({
  ...(await original<object>()),
  prepareUpload: vi.fn(async (file: File) => ({ file })),
}));
const mounted: VueWrapper[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  vi.unstubAllGlobals();
});
const snapshot = (version = 1n, set = true): Avatar => ({
  $typeName: 'user.v1.Avatar',
  subjectId: new Uint8Array(16).fill(1),
  version,
  fileId: set ? new Uint8Array(16).fill(2) : new Uint8Array(),
});
async function fixture(
  image: Promise<Blob> = Promise.resolve(new Blob(['verified'], { type: 'image/png' })),
  wholePage = false,
) {
  const state = shallowRef<SessionState>({
    status: 'authenticated',
    generation: 'one',
    subjectId: '01'.repeat(16),
  });
  const session = {
    state,
    bootstrap: vi.fn(async () => {
      state.value = { ...state.value, status: 'checking' };
      await Promise.resolve();
      state.value = { ...state.value, status: 'authenticated' };
    }),
    capture: () => ({ generation: state.value.generation, subjectId: state.value.subjectId }),
    readOwned: vi.fn(async (action) => action()),
    writeOwned: vi.fn(async (action) => action()),
    readProfile: vi.fn().mockResolvedValue({
      $typeName: 'user.v1.Profile',
      subjectId: new Uint8Array(16).fill(1),
      version: 1n,
      displayName: 'Анна',
      lastName: '',
      birthDate: '',
      gender: 0,
      phone: '',
      city: '',
      bio: '',
      showAge: false,
      createdAtUnix: 0n,
      updatedAtUnix: 0n,
    }),
    withSession: vi.fn(async (_guard, action: () => Promise<unknown>) => action()),
  } as unknown as SessionController;
  const api = {
    get: vi.fn().mockResolvedValue(snapshot()),
    clear: vi.fn(),
    set: vi.fn(),
    create: vi.fn(),
    put: vi.fn(),
    complete: vi.fn(),
    status: vi.fn(),
    remove: vi.fn(),
    image: vi.fn().mockReturnValue(image),
  } as AvatarApi;
  const createURL = vi.fn(() => 'blob:verified');
  const revokeURL = vi.fn();
  vi.stubGlobal(
    'URL',
    class extends URL {
      static createObjectURL = createURL;
      static revokeObjectURL = revokeURL;
    },
  );
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: IdView }],
  });
  await router.push('/');
  const wrapper = mount(wholePage ? IdView : AvatarEditor, {
    props: { initials: 'АБ' },
    global: {
      plugins: [router],
      provide: {
        [sessionKey as symbol]: session,
        [accountKey as symbol]: session,
        [avatarApiKey as symbol]: api,
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  return { state, session, api, wrapper, createURL, revokeURL };
}
function button(wrapper: VueWrapper, text: string) {
  const result = wrapper.findAll('button').find((b) => b.text() === text);
  if (!result) throw new Error(`Missing button ${text}`);
  return result;
}
const labels = (wrapper: VueWrapper) =>
  wrapper.findAll('.avatar-actions > button').map((b) => b.text());
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
function dialogButton(text: string) {
  const result = Array.from(dialog()?.querySelectorAll('button') ?? []).find(
    (b) => b.textContent?.trim() === text,
  );
  if (!result) throw new Error(`Missing dialog button ${text}`);
  return result;
}
async function pick(wrapper: VueWrapper, type = 'image/png') {
  const input = wrapper.get('input[type="file"]');
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: [new File(['png'], 'avatar.png', { type })],
  });
  await input.trigger('change');
  await flushPromises();
}
const next = new Uint8Array(16).fill(3);
type Created = Awaited<ReturnType<AvatarApi['create']>>;
type Status = Awaited<ReturnType<AvatarApi['status']>>;
function created(state: FileState, fileId = next): Created {
  return {
    $typeName: 'files.v1.CreateUploadResponse',
    uploadId: 'upload',
    receivedParts: [],
    uploadExpiresAtUnix: 0n,
    fileId,
    state,
    parts: [],
  };
}
/** Выбор файла, превью и «Сохранить фото»: всё, что делает покупатель. */
async function saveChosen(wrapper: VueWrapper) {
  await pick(wrapper);
  expect(dialog()?.textContent).toContain('Новое фото профиля');
  dialogButton('Сохранить фото').click();
  await flushPromises();
}
function readyUpload(api: AvatarApi) {
  vi.mocked(api.create).mockResolvedValue(created(FileState.SCANNING));
  vi.mocked(api.status).mockResolvedValue({ state: FileState.READY } as Status);
}

describe('profile photo', () => {
  it('saves a chosen photo after the preview without intermediate buttons', async () => {
    const { wrapper, api } = await fixture();
    readyUpload(api);
    vi.mocked(api.set).mockResolvedValue({ ...snapshot(2n), fileId: next });
    expect(labels(wrapper)).toEqual(['Изменить фото', 'Удалить фото']);
    await saveChosen(wrapper);
    expect(dialog()).toBeNull();
    expect(api.set).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.set).mock.calls[0]?.slice(0, 2)).toEqual([next, 1n]);
    expect(wrapper.get('[role="status"]').text()).toBe('Фото сохранено.');
    expect(labels(wrapper)).toEqual(['Изменить фото', 'Удалить фото']);
  });
  it('renews an expired session once and repeats the upload under the same key', async () => {
    const { wrapper, api, session } = await fixture();
    readyUpload(api);
    vi.mocked(api.create).mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated));
    vi.mocked(api.set).mockResolvedValue({ ...snapshot(2n), fileId: next });
    await saveChosen(wrapper);
    expect(session.bootstrap).toHaveBeenCalledTimes(1);
    expect(api.create).toHaveBeenCalledTimes(2);
    const [first, second] = vi.mocked(api.create).mock.calls.map((call) => call[0]);
    expect(second).toBe(first);
    expect(api.set).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[role="status"]').text()).toBe('Фото сохранено.');
  });
  it('treats a rejected photo as final: discards it and leaves the saved photo', async () => {
    const { wrapper, api } = await fixture();
    vi.mocked(api.create).mockResolvedValue(created(FileState.SCANNING));
    vi.mocked(api.status).mockResolvedValue({ state: FileState.REJECTED } as Status);
    await saveChosen(wrapper);
    expect(api.set).not.toHaveBeenCalled();
    expect(api.remove).toHaveBeenCalledWith(next, expect.anything());
    expect(wrapper.get('[role="alert"]').text()).toContain('Фото не прошло проверку');
    expect(wrapper.find('.reconcile-panel').exists()).toBe(false);
    expect(button(wrapper, 'Изменить фото').attributes('aria-disabled')).toBe('false');
    expect(wrapper.find('img').attributes('src')).toBe('blob:verified');
  });
  it('never opens the preview for an unsupported file', async () => {
    const { wrapper, api } = await fixture();
    await pick(wrapper, 'image/gif');
    expect(dialog()).toBeNull();
    expect(api.create).not.toHaveBeenCalled();
    expect(wrapper.get('[role="alert"]').text()).toContain('PNG или JPEG');
  });
  it('rereads a lost write and repeats it once under the same version', async () => {
    const { wrapper, api } = await fixture();
    readyUpload(api);
    vi.mocked(api.set)
      .mockRejectedValueOnce(new ConnectError('lost', Code.Unavailable))
      .mockResolvedValueOnce({ ...snapshot(2n), fileId: next });
    await saveChosen(wrapper);
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.set).mock.calls.map((c) => c[1])).toEqual([1n, 1n]);
    expect(wrapper.get('[role="status"]').text()).toBe('Фото сохранено.');
  });
  it('recognises a lost write that landed without writing again', async () => {
    const { wrapper, api } = await fixture();
    readyUpload(api);
    vi.mocked(api.set).mockRejectedValueOnce(new ConnectError('lost', Code.Unavailable));
    vi.mocked(api.get).mockResolvedValue({ ...snapshot(2n), fileId: next });
    await saveChosen(wrapper);
    expect(api.set).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[role="status"]').text()).toBe('Фото сохранено.');
  });
  it('asks the buyer only when the reread also fails, with one check action', async () => {
    const { wrapper, api } = await fixture();
    readyUpload(api);
    vi.mocked(api.set).mockRejectedValueOnce(new ConnectError('lost', Code.Unavailable));
    vi.mocked(api.get).mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    await saveChosen(wrapper);
    expect(wrapper.get('.reconcile-panel').text()).toContain('не знаем, сохранилось ли');
    expect(button(wrapper, 'Изменить фото').attributes('aria-disabled')).toBe('true');
    vi.mocked(api.get).mockResolvedValue({ ...snapshot(2n), fileId: next });
    await button(wrapper, 'Проверить ещё раз').trigger('click');
    await flushPromises();
    expect(api.set).toHaveBeenCalledTimes(1);
    expect(wrapper.find('.reconcile-panel').exists()).toBe(false);
    expect(wrapper.get('[role="status"]').text()).toBe('Фото сохранено.');
  });
  it('lets another tab win a conflict and discards the unused photo', async () => {
    const { wrapper, api } = await fixture();
    readyUpload(api);
    vi.mocked(api.set).mockRejectedValueOnce(new ConnectError('conflict', Code.Aborted));
    vi.mocked(api.get).mockResolvedValue({ ...snapshot(2n), fileId: new Uint8Array(16).fill(9) });
    await saveChosen(wrapper);
    expect(api.set).toHaveBeenCalledTimes(1);
    expect(api.remove).toHaveBeenCalledWith(next, expect.anything());
    expect(wrapper.get('[role="alert"]').text()).toContain('в другой вкладке');
  });
  it('removes the photo only after confirmation and keeps focus on the photo action', async () => {
    const { wrapper, api, revokeURL } = await fixture();
    vi.mocked(api.clear).mockResolvedValue(snapshot(2n, false));
    const focus = vi.spyOn(button(wrapper, 'Изменить фото').element, 'focus');
    await button(wrapper, 'Удалить фото').trigger('click');
    dialogButton('Оставить фото').click();
    await flushPromises();
    expect(api.clear).not.toHaveBeenCalled();
    await button(wrapper, 'Удалить фото').trigger('click');
    dialogButton('Удалить фото').click();
    await flushPromises();
    expect(api.clear).toHaveBeenCalledTimes(1);
    expect(wrapper.find('img').exists()).toBe(false);
    expect(revokeURL).toHaveBeenCalledWith('blob:verified');
    expect(labels(wrapper)).toEqual(['Добавить фото']);
    expect(focus).toHaveBeenCalled();
    expect(button(wrapper, 'Добавить фото').element).toBe(focus.mock.contexts.at(-1));
  });
  it('recovers an expired cookie for reconciliation without repeating the deletion', async () => {
    const { wrapper, api, session } = await fixture();
    vi.mocked(api.clear).mockRejectedValue(new ConnectError('expired', Code.Unauthenticated));
    vi.mocked(api.get)
      .mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated))
      .mockResolvedValue(snapshot(2n, false));
    await button(wrapper, 'Удалить фото').trigger('click');
    dialogButton('Удалить фото').click();
    await flushPromises();
    expect(session.bootstrap).toHaveBeenCalledTimes(1);
    expect(api.get).toHaveBeenCalledTimes(3);
    expect(api.clear).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[role="status"]').text()).toContain('Фото удалено');
  });
  it('keeps the photo mounted within ID during same-owner session recovery', async () => {
    const { wrapper, api, session, state } = await fixture(undefined, true);
    const editor = wrapper.findComponent(AvatarEditor).vm.$;
    let resume!: () => void;
    vi.mocked(session.bootstrap).mockImplementation(async () => {
      state.value = { ...state.value, status: 'checking' };
      await new Promise<void>((resolve) => {
        resume = resolve;
      });
      state.value = { ...state.value, status: 'authenticated' };
    });
    vi.mocked(api.clear).mockRejectedValue(new ConnectError('lost', Code.Unavailable));
    vi.mocked(api.get).mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated));
    await button(wrapper, 'Удалить фото').trigger('click');
    dialogButton('Удалить фото').click();
    await flushPromises();
    expect(session.bootstrap).toHaveBeenCalledTimes(1);
    expect(wrapper.findComponent(AvatarEditor).vm.$).toBe(editor);
    expect(wrapper.find('.id-content').attributes('style')).toContain('display: none');
    expect(wrapper.find('.id-content').attributes('inert')).toBeDefined();
    resume();
    await flushPromises();
    expect(wrapper.findComponent(AvatarEditor).vm.$).toBe(editor);
    expect(api.get).toHaveBeenCalledTimes(3);
  });
  it('discards the old owner when session recovery switches identity', async () => {
    const { wrapper, api, session, state } = await fixture();
    vi.mocked(api.clear).mockRejectedValue(new ConnectError('lost', Code.Unavailable));
    vi.mocked(api.get).mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated));
    vi.mocked(session.bootstrap).mockImplementation(async () => {
      state.value = { status: 'anonymous', generation: 'new', subjectId: null };
    });
    await button(wrapper, 'Удалить фото').trigger('click');
    dialogButton('Удалить фото').click();
    await flushPromises();
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(wrapper.find('img').exists()).toBe(false);
    expect(dialog()).toBeNull();
  });
  it('lets personal data be edited while a photo is being saved', async () => {
    const { wrapper, api } = await fixture(undefined, true);
    vi.mocked(api.create).mockReturnValue(new Promise(() => {}));
    await saveChosen(wrapper);
    expect(wrapper.get('.avatar-progress').text()).toBe('Сохраняем фото…');
    const edit = button(wrapper, 'Изменить данные');
    expect(edit.attributes('disabled')).toBeUndefined();
    expect(edit.attributes('aria-disabled')).toBeUndefined();
    await edit.trigger('click');
    expect(wrapper.find('#id-city').exists()).toBe(true);
    // Второе фото не выбрать, пока сохраняется первое.
    expect(button(wrapper, 'Изменить фото').attributes('aria-disabled')).toBe('true');
  });
  it('keeps the photo available while personal data are being edited', async () => {
    const { wrapper } = await fixture(undefined, true);
    await button(wrapper, 'Изменить данные').trigger('click');
    expect(button(wrapper, 'Изменить фото').attributes('aria-disabled')).toBe('false');
  });
  it('discards a late image after logout and clears all object URLs', async () => {
    let resolve!: (value: Blob) => void;
    const pending = new Promise<Blob>((r) => {
      resolve = r;
    });
    const { wrapper, state, createURL } = await fixture(pending);
    state.value = { status: 'anonymous', generation: 'two', subjectId: null };
    await flushPromises();
    resolve(new Blob(['private'], { type: 'image/png' }));
    await flushPromises();
    expect(createURL).not.toHaveBeenCalled();
    expect(wrapper.find('img').exists()).toBe(false);
  });
  it('releases the verified blob on unmount instead of retaining a capability URL', async () => {
    const { wrapper, revokeURL } = await fixture();
    expect(wrapper.find('img').attributes('src')).toBe('blob:verified');
    wrapper.unmount();
    expect(revokeURL).toHaveBeenCalledWith('blob:verified');
  });
});
