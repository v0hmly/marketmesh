import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { AccountSettings, ThemePreference } from '../../../shared/api/types';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';
import App from '../../../App.vue';
vi.mock('../../../shared/features', () => ({
  addressesEnabled: false,
  settingsEnabled: true,
  ordersEnabled: false,
  favoritesEnabled: false,
  reviewsEnabled: false,
  idEnabled: false,
}));
const settings = (theme: ThemePreference = 'system', version = 1n): AccountSettings => ({
  subjectId: new Uint8Array(16).fill(1),
  version,
  theme,
});
function fixture(initial: SessionState['status'] = 'authenticated') {
  const state = shallowRef<SessionState>({
    status: initial,
    generation: 'g1',
    subjectId: '01'.repeat(16),
  });
  const session: SessionController = {
    state,
    bootstrap: vi.fn(),
    register: vi.fn(),
    startLogin: vi.fn(),
    completeLogin: vi.fn(),
    resendLoginCode: vi.fn(),
    requestEmailVerification: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    dispose: vi.fn(),
    capture: vi.fn(() => ({
      generation: state.value.generation,
      subjectId: state.value.subjectId,
    })),
    readProfile: vi.fn(),
    updateProfile: vi.fn(),
    readAddresses: vi.fn(),
    createAddress: vi.fn(),
    updateAddress: vi.fn(),
    deleteAddress: vi.fn(),
    setDefaultAddress: vi.fn(),
    readSettings: vi.fn().mockResolvedValue(settings()),
    updateSettings: vi.fn().mockResolvedValue(settings('dark', 2n)),
  };
  return { session, state };
}
const mounted: VueWrapper[] = [];
beforeEach(() => vi.spyOn(window, 'scrollTo').mockImplementation(() => {}));
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  vi.useRealTimers();
});
async function open(session: SessionController) {
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push('/account/settings');
  await router.isReady();
  const wrapper = mount(App, {
    global: { plugins: [router], provide: { [sessionKey as symbol]: session } },
  });
  mounted.push(wrapper);
  await flushPromises();
  return { wrapper, router };
}
function button(wrapper: VueWrapper, text: string) {
  const item = wrapper.findAll('button').find((candidate) => candidate.text().includes(text));
  if (!item) throw new Error('Missing button');
  return item;
}
async function save(wrapper: VueWrapper) {
  await wrapper.find('form').trigger('submit');
  await flushPromises();
}
describe('account settings form', () => {
  it('changes the shell theme only on confirmed save and uses independent settings CAS', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    await wrapper.find('input[value="dark"]').setValue(true);
    expect(document.documentElement.dataset.themePreference).toBe('system');
    await save(wrapper);
    expect(session.updateSettings).toHaveBeenCalledWith(
      { theme: 'dark', expectedVersion: 1n },
      { generation: 'g1', subjectId: '01'.repeat(16) },
    );
    expect(document.documentElement.dataset.themePreference).toBe('dark');
    expect(wrapper.text()).toContain('Оформление сохранено.');
    expect(session.updateProfile).not.toHaveBeenCalled();
  });
  it('preserves the draft and requires reread plus explicit choice after a CAS conflict', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(
      new ConnectError('private', Code.Aborted),
    );
    const { wrapper } = await open(session);
    await wrapper.find('input[value="dark"]').setValue(true);
    await save(wrapper);
    await save(wrapper);
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('light', 7n));
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    expect((wrapper.find('input[value="dark"]').element as HTMLInputElement).checked).toBe(true);
    expect(document.documentElement.dataset.themePreference).toBe('light');
    await button(wrapper, 'Оставить мой выбор').trigger('click');
    await save(wrapper);
    expect(vi.mocked(session.updateSettings).mock.calls[1]?.[0].expectedVersion).toBe(7n);
  });
  it('never retries an unknown write, and can accept the current server settings', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(new TypeError('private network'));
    const { wrapper } = await open(session);
    await wrapper.find('input[value="dark"]').setValue(true);
    await save(wrapper);
    await save(wrapper);
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
    expect(document.documentElement.dataset.themePreference).toBe('system');
    expect(wrapper.text()).not.toContain('private network');
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('dark', 2n));
    await button(wrapper, 'Перечитать').trigger('click');
    await flushPromises();
    await button(wrapper, 'Принять актуальные настройки').trigger('click');
    expect((wrapper.find('input[value="dark"]').element as HTMLInputElement).checked).toBe(true);
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
    expect(document.documentElement.dataset.themePreference).toBe('dark');
  });
  it('clears a draft and rejects a late write after owner change', async () => {
    const { session, state } = fixture();
    let resolve!: (value: AccountSettings) => void;
    vi.mocked(session.updateSettings).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const { wrapper } = await open(session);
    await wrapper.find('input[value="dark"]').setValue(true);
    await save(wrapper);
    state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
    await flushPromises();
    resolve(settings('dark', 2n));
    await flushPromises();
    expect(wrapper.find('form').exists()).toBe(false);
    expect(document.documentElement.dataset.themePreference).toBe('system');
    expect(wrapper.text()).not.toContain('Оформление сохранено.');
  });
  it('warns before losing a draft, keeps it across refresh and bounds pending polls', async () => {
    const { session, state } = fixture();
    const { wrapper, router } = await open(session);
    await wrapper.find('input[value="light"]').setValue(true);
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    await router.push('/login');
    expect(router.currentRoute.value.path).toBe('/account/settings');
    state.value = { ...state.value, status: 'checking' };
    await flushPromises();
    state.value = { ...state.value, status: 'authenticated' };
    await flushPromises();
    expect((wrapper.find('input[value="light"]').element as HTMLInputElement).checked).toBe(true);
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const pending = fixture('profilePending');
    vi.mocked(pending.session.readProfile).mockRejectedValue(
      new ConnectError('pending', Code.NotFound),
    );
    const view = await open(pending.session);
    for (const delay of [1500, 3000, 6000, 12000]) {
      await vi.advanceTimersByTimeAsync(delay);
      await flushPromises();
    }
    expect(pending.session.readProfile).toHaveBeenCalledTimes(4);
    expect(view.wrapper.text()).toContain('Автоматическая проверка приостановлена');
  });
});
