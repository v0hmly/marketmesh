import { afterEach, describe, expect, it, vi } from 'vitest';
import { shallowRef } from 'vue';
import { flushPromises } from '@vue/test-utils';
import type { AccountSettings, ThemePreference } from '../shared/api/types';
import type { SessionController, SessionState } from './session';
import { createThemeController, type ThemeController } from './theme';
const settings = (theme: ThemePreference = 'dark', version = 1n, id = 1): AccountSettings => ({
  subjectId: new Uint8Array(16).fill(id),
  version,
  theme,
});
function fixture() {
  const state = shallowRef<SessionState>({
    status: 'authenticated',
    subjectId: '01'.repeat(16),
    generation: 'g1',
  });
  const session: SessionController = {
    state,
    bootstrap: vi.fn(),
    capture: vi.fn(() => ({
      generation: state.value.generation,
      subjectId: state.value.subjectId,
    })),
    register: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    dispose: vi.fn(),
    readProfile: vi.fn(),
    updateProfile: vi.fn(),
    readAddresses: vi.fn(),
    createAddress: vi.fn(),
    updateAddress: vi.fn(),
    deleteAddress: vi.fn(),
    setDefaultAddress: vi.fn(),
    readSettings: vi.fn().mockResolvedValue(settings()),
    updateSettings: vi.fn(),
  };
  return { state, session };
}
const controllers: ThemeController[] = [];
function theme(session: SessionController) {
  const controller = createThemeController(session);
  controllers.push(controller);
  return controller;
}
afterEach(() => {
  for (const controller of controllers.splice(0)) controller.dispose();
  vi.unstubAllGlobals();
});
describe('shell confirmed account theme', () => {
  it('loads outside settings pages and resets synchronously on logout and owner change', async () => {
    const { session, state } = fixture();
    const controller = theme(session);
    await flushPromises();
    expect(controller.preference.value).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
    expect(controller.preference.value).toBe('system');
    vi.mocked(session.readSettings).mockResolvedValue(settings('light', 1n, 2));
    state.value = { status: 'authenticated', generation: 'g3', subjectId: '02'.repeat(16) };
    await flushPromises();
    expect(controller.preference.value).toBe('light');
    state.value = { ...state.value, status: 'uncertain' };
    expect(controller.preference.value).toBe('system');
  });
  it('rejects late owner responses and older versions after a confirmed write', async () => {
    const { session, state } = fixture();
    let resolve!: (value: AccountSettings) => void;
    vi.mocked(session.readSettings).mockReturnValueOnce(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const controller = theme(session);
    controller.accept(settings('dark', 3n), session.capture());
    resolve(settings('light', 1n));
    await flushPromises();
    expect(controller.preference.value).toBe('dark');
    let old!: (value: AccountSettings) => void;
    vi.mocked(session.readSettings).mockReturnValueOnce(
      new Promise((done) => {
        old = done;
      }),
    );
    const pending = controller.load();
    vi.mocked(session.readSettings).mockResolvedValue(settings('light', 1n, 2));
    state.value = { status: 'authenticated', generation: 'g2', subjectId: '02'.repeat(16) };
    await flushPromises();
    old(settings('dark', 4n));
    await pending;
    expect(controller.preference.value).toBe('light');
  });
  it('follows system changes only for system preference and preserves a same-owner refresh', async () => {
    const media = Object.assign(new EventTarget(), { matches: false });
    vi.stubGlobal('matchMedia', () => media);
    const { session, state } = fixture();
    vi.mocked(session.readSettings).mockResolvedValue(settings('system'));
    const controller = theme(session);
    await flushPromises();
    expect(document.documentElement.dataset.theme).toBe('light');
    media.matches = true;
    media.dispatchEvent(new Event('change'));
    expect(document.documentElement.dataset.theme).toBe('dark');
    controller.accept(settings('light', 2n), session.capture());
    media.dispatchEvent(new Event('change'));
    expect(document.documentElement.dataset.theme).toBe('light');
    state.value = { ...state.value, status: 'checking' };
    expect(controller.preference.value).toBe('light');
    state.value = { ...state.value, status: 'authenticated' };
    expect(session.readSettings).toHaveBeenCalledTimes(1);
  });
  it('reports a read failure without retry loops, permits retry and ignores a stale failure after confirmation', async () => {
    const { session } = fixture();
    vi.mocked(session.readSettings).mockRejectedValueOnce(new Error('private'));
    const controller = theme(session);
    await flushPromises();
    expect(controller.failed.value).toBe(true);
    expect(session.readSettings).toHaveBeenCalledTimes(1);
    await controller.load();
    expect(controller.preference.value).toBe('dark');
    expect(controller.failed.value).toBe(false);
    let reject!: (error: Error) => void;
    vi.mocked(session.readSettings).mockReturnValueOnce(
      new Promise((_resolve, fail) => {
        reject = fail;
      }),
    );
    const pending = controller.load();
    controller.accept(settings('light', 2n), session.capture());
    reject(new Error('late'));
    await pending;
    expect(controller.failed.value).toBe(false);
  });
});
