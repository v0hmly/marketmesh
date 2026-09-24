import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { AccountSettings, Profile, ThemePreference } from '../../../shared/api/types';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';
import App from '../../../App.vue';
vi.mock('../../../shared/features', () => ({
  avatarEnabled: false,
  addressesEnabled: true,
  settingsEnabled: true,
  ordersEnabled: true,
  favoritesEnabled: true,
  reviewsEnabled: true,
  idEnabled: true,
  sellerEnabled: false,
  staffEnabled: false,
}));
const security = vi.hoisted(() => ({
  getCredentials: vi.fn(),
  listSessions: vi.fn(),
  changePassword: vi.fn(),
  startEmailChange: vi.fn(),
  startLoginCodeChange: vi.fn(),
  completeLoginCodeChange: vi.fn(),
  revokeSession: vi.fn(),
  logoutAll: vi.fn(),
}));
vi.mock('../security/api', async (original) => ({
  ...(await original<object>()),
  createSecurityApi: () => security,
}));

const settings = (theme: ThemePreference = 'system', version = 1n): AccountSettings => ({
  subjectId: new Uint8Array(16).fill(1),
  version,
  theme,
});
const profile = (): Profile => ({
  $typeName: 'user.v1.Profile',
  subjectId: new Uint8Array(16).fill(1),
  displayName: 'Вера',
  bio: '',
  version: 7n,
  createdAtUnix: 1n,
  updatedAtUnix: 2n,
  lastName: 'Ильина',
  birthDate: '',
  gender: 0,
  phone: '',
  city: 'Санкт-Петербург',
  showAge: false,
});
function fixture(initial: SessionState['status'] = 'authenticated') {
  const state = shallowRef<SessionState>({
    status: initial,
    generation: 'g1',
    subjectId: initial === 'anonymous' ? null : '01'.repeat(16),
  });
  const session: SessionController = {
    withSession: vi.fn(async (_guard, action) => action()),
    endSession: vi.fn(async (action) => action()),
    state,
    bootstrap: vi.fn().mockResolvedValue(undefined),
    register: vi.fn(),
    startLogin: vi.fn(),
    completeLogin: vi.fn(),
    resendLoginCode: vi.fn(),
    requestEmailVerification: vi.fn(),
    login: vi.fn(),
    logout: vi.fn().mockResolvedValue(undefined),
    dispose: vi.fn(),
    capture: vi.fn(() => ({
      generation: state.value.generation,
      subjectId: state.value.subjectId,
    })),
    readProfile: vi.fn().mockResolvedValue(profile()),
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
beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {});
  security.getCredentials.mockResolvedValue({
    email: 'vera@example.com',
    emailVerified: true,
    loginCodeEnabled: false,
    newDeviceCooldownUntilUnix: 0n,
  });
  security.listSessions.mockResolvedValue({ sessions: [] });
});
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  vi.useRealTimers();
});
async function open(session: SessionController, path = '/account/orders') {
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push(path);
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
  if (!item) throw new Error(`Missing button ${text}`);
  return item;
}
const picker = (wrapper: VueWrapper) => wrapper.find<HTMLSelectElement>('#theme-pick');
/** Выбор и уход из списка: blur сохраняет сразу, без паузы для перебора стрелками. */
async function choose(wrapper: VueWrapper, theme: ThemePreference) {
  await picker(wrapper).setValue(theme);
  await picker(wrapper).trigger('blur');
  await flushPromises();
}

describe('account layout', () => {
  it('lists five sections vertically with counters and marks the active one', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    const links = wrapper.findAll('.account-nav a');
    expect(links.map((link) => link.find('span').text())).toEqual([
      'Заказы',
      'Избранное',
      'Отзывы',
      'Адреса доставки',
      'MarketMesh ID',
    ]);
    expect(links[0]!.text()).toContain('4');
    expect(links[1]!.text()).toContain('6');
    expect(links[2]!.find('.account-nav-count').text()).toBe(', ждут отзыва: 2');
    expect(links[0]!.attributes('aria-current')).toBe('page');
    expect(links.filter((link) => link.attributes('aria-current'))).toHaveLength(1);
    expect(wrapper.text()).not.toContain('О себе');
    expect(wrapper.text()).not.toContain('Оформление');
    expect(wrapper.find('label[for="theme-pick"]').text()).toBe('Тема оформления');
    expect(button(wrapper, 'Выйти из аккаунта').exists()).toBe(true);
    expect(wrapper.find('.session-controls').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('Выйти на всех устройствах');
    expect(wrapper.find('h1').text()).toBe('Заказы');
    expect(wrapper.find('.eyebrow').exists()).toBe(false);
    expect(wrapper.find('.section-number').exists()).toBe(false);
    expect(wrapper.find('.footer-meta').text()).toBe('Личный кабинет');
    expect(wrapper.find('.brand').text()).toContain('MarketMesh.');
  });
  it('redirects removed sections into the cabinet', async () => {
    const { session } = fixture();
    const { router } = await open(session, '/account');
    expect(router.currentRoute.value.fullPath).toBe('/account/orders');
    await router.push('/account/settings');
    expect(router.currentRoute.value.fullPath).toBe('/account/orders');
    await router.push('/account/security');
    expect(router.currentRoute.value.fullPath).toBe('/account/id#security');
    await router.push('/account/security/verify');
    expect(router.currentRoute.value.name).toBe('security-link');
  });
  it('shows the sidebar only to the owner and names the area in the footer', async () => {
    const { session } = fixture('anonymous');
    const { wrapper, router } = await open(session);
    expect(wrapper.find('.account-sidebar').exists()).toBe(false);
    expect(wrapper.text()).toContain('Личное начинается со входа');
    await router.push('/login');
    await flushPromises();
    expect(wrapper.find('.footer-meta').text()).toBe('Вход');
  });
  it('keeps the sidebar and sign-out while a reloaded profile is still pending', async () => {
    const { session, state } = fixture();
    state.value = { status: 'profilePending', generation: 'g1', subjectId: null };
    const { wrapper } = await open(session, '/account/id');
    expect(wrapper.find('.account-sidebar').exists()).toBe(true);
    expect(picker(wrapper).attributes('disabled')).toBeDefined();
    await button(wrapper, 'Выйти из аккаунта').trigger('click');
    await flushPromises();
    expect(session.logout).toHaveBeenCalledWith(false);
  });
  it('forgets the previous public line when another owner takes the same generation', async () => {
    const { session, state } = fixture();
    const { wrapper } = await open(session, '/account/reviews');
    await button(wrapper, 'Написать отзыв на «Плед из шерсти мериноса»').trigger('click');
    expect(wrapper.find('.form-footer .field-help').text()).toContain('Вера, Санкт-Петербург');
    vi.mocked(session.readProfile).mockResolvedValue({
      ...profile(),
      subjectId: new Uint8Array(16).fill(2),
      displayName: 'Борис',
      city: 'Тверь',
    });
    state.value = { status: 'authenticated', generation: 'g1', subjectId: '02'.repeat(16) };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Вера, Санкт-Петербург');
    await button(wrapper, 'Написать отзыв на «Плед из шерсти мериноса»').trigger('click');
    expect(wrapper.find('.form-footer .field-help').text()).toContain('Борис, Тверь');
  });
  it('keeps the shared sign-out only outside the cabinet layout', async () => {
    const { session } = fixture();
    const { wrapper, router } = await open(session);
    expect(wrapper.find('.session-controls').exists()).toBe(false);
    await router.push('/account/security/verify');
    await flushPromises();
    expect(wrapper.find('.account-sidebar').exists()).toBe(false);
    expect(wrapper.find('.session-controls').exists()).toBe(true);
    expect(wrapper.find('.footer-meta').text()).toBe('Безопасность аккаунта');
    // Выход везде закрывает и этот сеанс — подпись говорит об этом прямо.
    expect(button(wrapper, 'Выйти на всех устройствах').attributes('aria-describedby')).toBe(
      'session-controls-all-help',
    );
    expect(wrapper.find('#session-controls-all-help').text()).toBe(
      'Выход на всех устройствах закроет и этот сеанс.',
    );
  });
  it.each([
    ['/account/orders', 'Заказы появятся'],
    ['/account/favorites', 'Избранное появится'],
    ['/account/reviews', 'Отзывы появятся'],
  ])('waits for a pending profile on %s instead of asking to sign in', async (path, text) => {
    const { session, state } = fixture('profilePending');
    vi.mocked(session.readProfile).mockImplementation(async () => {
      state.value = { status: 'authenticated', generation: 'g1', subjectId: '01'.repeat(16) };
      return profile();
    });
    const { wrapper } = await open(session, path);
    expect(wrapper.find('.account-sidebar').exists()).toBe(true);
    expect(wrapper.text()).toContain('Готовим ваш аккаунт');
    expect(wrapper.text()).toContain(text);
    expect(wrapper.text()).not.toContain('Личное начинается со входа');
    await button(wrapper, 'Проверить готовность').trigger('click');
    await flushPromises();
    expect(session.readProfile).toHaveBeenCalled();
    expect(wrapper.text()).not.toContain('Готовим ваш аккаунт');
    expect(wrapper.text()).not.toContain('Личное начинается со входа');
  });
  it('shows the actual public line from MarketMesh ID under the review form', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session, '/account/reviews');
    await button(wrapper, 'Написать отзыв на «Плед из шерсти мериноса»').trigger('click');
    const hint = wrapper.find('.form-footer .field-help');
    expect(hint.text()).toBe(
      'Рядом с отзывом покажем: Вера, Санкт-Петербург. Изменить — в MarketMesh ID.',
    );
    expect(hint.find('a').attributes('href')).toBe('/account/id');
  });
  it('updates a counter after the section changes its list', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session, '/account/favorites');
    const favorites = () => wrapper.findAll('.account-nav a')[1]!;
    expect(favorites().text()).toContain('6');
    await wrapper.findAll('.favorite-card button.text-button')[0]!.trigger('click');
    expect(favorites().text()).toContain('5');
  });
});

describe('theme picker in the sidebar', () => {
  it('saves immediately with the confirmed version and applies the theme', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    expect(picker(wrapper).element.value).toBe('system');
    await choose(wrapper, 'dark');
    expect(session.updateSettings).toHaveBeenCalledWith(
      { theme: 'dark', expectedVersion: 1n },
      { generation: 'g1', subjectId: '01'.repeat(16) },
    );
    expect(document.documentElement.dataset.themePreference).toBe('dark');
    expect(wrapper.find('#theme-pick-result').text()).toBe(
      'Тема сохранена и применится на всех ваших устройствах.',
    );
    expect(session.updateProfile).not.toHaveBeenCalled();
  });
  it('rereads after a CAS conflict and shows the theme that is actually saved', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(
      new ConnectError('private', Code.Aborted),
    );
    const { wrapper } = await open(session);
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('light', 7n));
    await choose(wrapper, 'dark');
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
    expect(picker(wrapper).element.value).toBe('light');
    expect(document.documentElement.dataset.themePreference).toBe('light');
    expect(wrapper.find('#theme-pick-error').text()).toContain('изменили в другом окне');
    expect(wrapper.text()).not.toContain('private');
    vi.mocked(session.updateSettings).mockResolvedValueOnce(settings('dark', 8n));
    await choose(wrapper, 'dark');
    expect(vi.mocked(session.updateSettings).mock.calls[1]?.[0].expectedVersion).toBe(7n);
    expect(document.documentElement.dataset.themePreference).toBe('dark');
    expect(wrapper.find('#theme-pick-result').text()).toContain('Тема сохранена');
  });
  it('saves only the last of several quick keyboard steps', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const { session } = fixture();
    const { wrapper } = await open(session);
    await picker(wrapper).setValue('light');
    await vi.advanceTimersByTimeAsync(200);
    await picker(wrapper).setValue('dark');
    expect(picker(wrapper).attributes('disabled')).toBeUndefined();
    await vi.advanceTimersByTimeAsync(500);
    await flushPromises();
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
    expect(session.updateSettings).toHaveBeenCalledWith(
      { theme: 'dark', expectedVersion: 1n },
      expect.anything(),
    );
  });
  it('shows the theme the shell kept when the reply is older than the confirmed version', async () => {
    const { session } = fixture();
    vi.mocked(session.readSettings).mockResolvedValue(settings('system', 3n));
    vi.mocked(session.updateSettings).mockResolvedValueOnce(settings('dark', 2n));
    const { wrapper } = await open(session);
    await choose(wrapper, 'dark');
    expect(picker(wrapper).element.value).toBe('system');
    expect(document.documentElement.dataset.themePreference).toBe('system');
    expect(wrapper.find('#theme-pick-error').text()).toContain('Тему изменили в другом окне');
    expect(wrapper.find('#theme-pick-result').exists()).toBe(false);
  });
  it('never retries an unknown write and reports success once the reread confirms it', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(new TypeError('private network'));
    const { wrapper } = await open(session);
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('dark', 2n));
    await choose(wrapper, 'dark');
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
    expect(document.documentElement.dataset.themePreference).toBe('dark');
    expect(wrapper.find('#theme-pick-result').text()).toContain('Тема сохранена');
    expect(wrapper.text()).not.toContain('private network');
  });
  it('blocks new choices until an unknown write is checked', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(new TypeError('private network'));
    const { wrapper } = await open(session);
    vi.mocked(session.readSettings).mockRejectedValueOnce(new TypeError('offline'));
    await choose(wrapper, 'dark');
    expect(picker(wrapper).attributes('disabled')).toBeDefined();
    expect(wrapper.find('#theme-pick-error').text()).toContain('Не удалось проверить');
    expect(document.documentElement.dataset.themePreference).toBe('system');
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('system', 1n));
    await button(wrapper, 'Проверить тему').trigger('click');
    await flushPromises();
    expect(picker(wrapper).attributes('disabled')).toBeUndefined();
    expect(picker(wrapper).element.value).toBe('system');
    expect(wrapper.find('#theme-pick-error').text()).toContain('Выбор не сохранился');
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
  });
  it('settles an unknown write when the shell reread succeeds elsewhere', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(new TypeError('private network'));
    const { wrapper } = await open(session);
    vi.mocked(session.readSettings).mockRejectedValueOnce(new TypeError('offline'));
    await choose(wrapper, 'dark');
    expect(wrapper.find('#theme-pick-error').text()).toContain('Не удалось проверить');
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('dark', 2n));
    await button(wrapper, 'Повторить загрузку оформления').trigger('click');
    await flushPromises();
    expect(picker(wrapper).attributes('disabled')).toBeUndefined();
    expect(wrapper.find('#theme-pick-error').exists()).toBe(false);
    expect(wrapper.find('#theme-pick-result').text()).toContain('Тема сохранена');
    expect(session.updateSettings).toHaveBeenCalledTimes(1);
  });
  it('restores the saved theme after a definitive rejection', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(
      new ConnectError('private', Code.ResourceExhausted),
    );
    const { wrapper } = await open(session);
    await choose(wrapper, 'dark');
    expect(picker(wrapper).element.value).toBe('system');
    expect(wrapper.find('#theme-pick-error').text()).toContain('Слишком много запросов');
    expect(session.readSettings).toHaveBeenCalledTimes(1);
  });
  it('keeps a reply that arrives after leaving the cabinet', async () => {
    const { session } = fixture();
    let resolve!: (value: AccountSettings) => void;
    vi.mocked(session.updateSettings).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const { wrapper, router } = await open(session);
    await choose(wrapper, 'dark');
    await router.push('/account/security/verify');
    await flushPromises();
    expect(wrapper.find('#theme-pick').exists()).toBe(false);
    resolve(settings('dark', 2n));
    await flushPromises();
    // Сервер сохранил тему: shell применяет её и знает новую версию для следующей смены.
    expect(document.documentElement.dataset.themePreference).toBe('dark');
    await router.push('/account/orders');
    await flushPromises();
    expect(picker(wrapper).element.value).toBe('dark');
    vi.mocked(session.updateSettings).mockResolvedValue(settings('light', 3n));
    await choose(wrapper, 'light');
    expect(vi.mocked(session.updateSettings).mock.calls[1]![0]).toEqual({
      theme: 'light',
      expectedVersion: 2n,
    });
  });
  it('returns focus to the list once an unknown write is checked', async () => {
    const { session } = fixture();
    vi.mocked(session.updateSettings).mockRejectedValueOnce(new TypeError('private network'));
    vi.mocked(session.readSettings)
      .mockResolvedValueOnce(settings())
      .mockRejectedValueOnce(new TypeError('offline'));
    const router = createStorefrontRouter(createMemoryHistory());
    await router.push('/account/orders');
    await router.isReady();
    const wrapper = mount(App, {
      attachTo: document.body,
      global: { plugins: [router], provide: { [sessionKey as symbol]: session } },
    });
    mounted.push(wrapper);
    await flushPromises();
    picker(wrapper).element.focus();
    // A dispatched blur saves at once but leaves focus on the list, as after a keyboard choice.
    await choose(wrapper, 'dark');
    const retry = button(wrapper, 'Проверить тему');
    expect(document.activeElement).toBe(retry.element);
    vi.mocked(session.readSettings).mockResolvedValueOnce(settings('dark', 2n));
    await retry.trigger('click');
    await flushPromises();
    expect(wrapper.find('#theme-pick-result').text()).toContain('Тема сохранена');
    expect(document.activeElement).toBe(picker(wrapper).element);
  });
  it('drops a late write after an owner change', async () => {
    const { session, state } = fixture();
    let resolve!: (value: AccountSettings) => void;
    vi.mocked(session.updateSettings).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const { wrapper } = await open(session);
    await choose(wrapper, 'dark');
    state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
    await flushPromises();
    resolve(settings('dark', 2n));
    await flushPromises();
    expect(wrapper.find('#theme-pick').exists()).toBe(false);
    expect(document.documentElement.dataset.themePreference).toBe('system');
    expect(wrapper.text()).not.toContain('Тема сохранена');
  });
});

describe('sign out from the sidebar', () => {
  it('ends only this session and opens the login page', async () => {
    const { session } = fixture();
    const { wrapper, router } = await open(session);
    await button(wrapper, 'Выйти из аккаунта').trigger('click');
    await flushPromises();
    expect(session.logout).toHaveBeenCalledWith(false);
    expect(router.currentRoute.value.path).toBe('/login');
  });
  it('hides private state and never claims that a failed sign-out succeeded', async () => {
    const { session, state } = fixture();
    vi.mocked(session.logout).mockImplementation(async () => {
      state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
      await Promise.resolve();
      state.value = { status: 'unavailable', generation: 'g2', subjectId: null };
      throw new ConnectError('private network diagnostic', Code.Unavailable);
    });
    const { wrapper } = await open(session, '/account/id');
    expect(wrapper.text()).toContain('Вера Ильина');
    await button(wrapper, 'Выйти из аккаунта').trigger('click');
    await flushPromises();
    expect(wrapper.text()).not.toContain('Вера Ильина');
    expect(wrapper.find('.account-sidebar').exists()).toBe(false);
    expect(wrapper.text()).toContain('Сервер не подтвердил выход');
    expect(wrapper.text()).not.toContain('private network diagnostic');
  });
});
