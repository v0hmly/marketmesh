import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import IdView from '../views/IdView.vue';
import { Code, ConnectError } from '@connectrpc/connect';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { avatarApiKey, FileState, type AvatarApi, type Avatar } from '../avatar/api';
import AvatarEditor from './AvatarEditor.vue';

vi.mock('../../../shared/features', async (original) => ({
  ...(await original<object>()),
  avatarEnabled: true,
}));
vi.mock('../avatar/api', async (original) => ({
  ...(await original<object>()),
  prepareUpload: vi.fn(async (file: File) => ({ file })),
}));
// MarketMesh ID includes the sign-in section; its reads are not part of the avatar lifecycle.
vi.mock('../security/api', async (original) => ({
  ...(await original<object>()),
  createSecurityApi: () => ({
    getCredentials: vi.fn(() => new Promise(() => {})),
    listSessions: vi.fn(() => new Promise(() => {})),
  }),
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
      provide: { [sessionKey as symbol]: session, [avatarApiKey as symbol]: api },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  return { state, session, api, wrapper, createURL, revokeURL };
}
function button(wrapper: VueWrapper, text: string) {
  const result = wrapper.findAll('button').find((b) => b.text() === text);
  if (!result) throw new Error('Missing button');
  return result;
}

describe('avatar editor lifecycle', () => {
  it('recovers an expired cookie for reconciliation without repeating the deletion', async () => {
    const { wrapper, api, session } = await fixture();
    vi.mocked(api.clear).mockRejectedValue(new ConnectError('expired', Code.Unauthenticated));
    await button(wrapper, 'Удалить аватар').trigger('click');
    await flushPromises();
    expect(session.bootstrap).not.toHaveBeenCalled();
    vi.mocked(api.get).mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated));
    await button(wrapper, 'Обновить состояние').trigger('click');
    await flushPromises();
    expect(session.bootstrap).toHaveBeenCalledTimes(1);
    expect(api.get).toHaveBeenCalledTimes(3);
    expect(api.clear).toHaveBeenCalledTimes(1);
    expect(button(wrapper, 'Удалить аватар').attributes('disabled')).toBeUndefined();
  });
  it('keeps the editor mounted within ID during same-owner session recovery', async () => {
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
    vi.mocked(api.get).mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated));
    await button(wrapper, 'Обновить состояние').trigger('click');
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
    vi.mocked(api.get).mockRejectedValueOnce(new ConnectError('expired', Code.Unauthenticated));
    vi.mocked(session.bootstrap).mockImplementation(async () => {
      state.value = { status: 'anonymous', generation: 'new', subjectId: null };
    });
    await button(wrapper, 'Обновить состояние').trigger('click');
    await flushPromises();
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(wrapper.find('img').exists()).toBe(false);
  });
  it('does not carry READY to a rejected replacement and clears the native file after reconciliation', async () => {
    const { wrapper, api } = await fixture();
    const next = new Uint8Array(16).fill(3);
    const choose = async () => {
      const input = wrapper.get('input[type="file"]');
      Object.defineProperty(input.element, 'files', {
        configurable: true,
        value: [new File(['png'], 'avatar.png', { type: 'image/png' })],
      });
      await input.trigger('change');
      await wrapper.get('form').trigger('submit');
      await flushPromises();
    };
    vi.mocked(api.create).mockResolvedValue({
      $typeName: 'files.v1.CreateUploadResponse',
      uploadId: 'upload',
      receivedParts: [],
      uploadExpiresAtUnix: 0n,
      fileId: next,
      state: FileState.READY,
      parts: [],
    });
    vi.mocked(api.status).mockResolvedValue({ state: FileState.READY } as Awaited<
      ReturnType<AvatarApi['status']>
    >);
    vi.mocked(api.set).mockRejectedValueOnce(new ConnectError('lost', Code.Unavailable));
    await choose();
    // Browser retains its native value until a confirmed write or read clears it.
    const input = wrapper.get('input[type="file"]').element as HTMLInputElement;
    Object.defineProperty(input, 'value', {
      configurable: true,
      writable: true,
      value: 'avatar.png',
    });
    vi.mocked(api.get).mockResolvedValue({ ...snapshot(2n), fileId: next });
    await button(wrapper, 'Обновить состояние').trigger('click');
    await flushPromises();
    expect(input.value).toBe('');
    vi.mocked(api.create).mockResolvedValue({
      $typeName: 'files.v1.CreateUploadResponse',
      uploadId: 'upload',
      receivedParts: [],
      uploadExpiresAtUnix: 0n,
      fileId: new Uint8Array(16).fill(4),
      state: FileState.REJECTED,
      parts: [],
    });
    vi.mocked(api.status).mockResolvedValue({ state: FileState.REJECTED } as Awaited<
      ReturnType<AvatarApi['status']>
    >);
    await choose();
    await button(wrapper, 'Обновить состояние').trigger('click');
    await flushPromises();
    expect(wrapper.text()).not.toContain('Сохранить выбранный аватар');
    expect(api.set).toHaveBeenCalledTimes(1);
  });
  it('requires reconciliation after an ambiguous committed deletion and never repeats it', async () => {
    const { wrapper, api, revokeURL } = await fixture();
    vi.mocked(api.clear).mockRejectedValue(
      new ConnectError('private upstream detail', Code.Unavailable),
    );
    await button(wrapper, 'Удалить аватар').trigger('click');
    await flushPromises();
    expect(api.clear).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('Запись могла выполниться');
    expect(wrapper.text()).not.toContain('private upstream');
    expect(button(wrapper, 'Удалить аватар').attributes('disabled')).toBeDefined();
    vi.mocked(api.get).mockResolvedValue(snapshot(2n, false));
    await button(wrapper, 'Обновить состояние').trigger('click');
    await flushPromises();
    expect(wrapper.find('img').exists()).toBe(false);
    expect(revokeURL).toHaveBeenCalledWith('blob:verified');
    expect(api.clear).toHaveBeenCalledTimes(1);
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
