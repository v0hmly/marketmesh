import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import { Gender, type Profile } from '../../../shared/api/types';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';
vi.mock('../../../shared/features', () => ({
  avatarEnabled: false,
  addressesEnabled: false,
  settingsEnabled: false,
  ordersEnabled: false,
  favoritesEnabled: false,
  reviewsEnabled: false,
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

const profile = (overrides: Partial<Profile> = {}): Profile => ({
  $typeName: 'user.v1.Profile',
  subjectId: new Uint8Array(16).fill(1),
  displayName: 'Вера',
  bio: 'Люблю керамику',
  version: 7n,
  createdAtUnix: 1571702400n,
  updatedAtUnix: 2n,
  lastName: 'Ильина',
  birthDate: '1989-03-12',
  gender: Gender.FEMALE,
  phone: '+7 921 000-11-22',
  city: 'Санкт-Петербург',
  showAge: false,
  ...overrides,
});
function fixture(initial: SessionState['status'] = 'authenticated') {
  const state = shallowRef<SessionState>({
    status: initial,
    generation: 'g1',
    subjectId: initial === 'authenticated' ? '01'.repeat(16) : null,
  });
  const session: SessionController = {
    withSession: vi.fn(async (_guard, action) => action()),
    endSession: vi.fn(async (action) => action()),
    state,
    bootstrap: vi.fn().mockResolvedValue(undefined),
    confirmEmail: vi.fn(async () => false),
    register: vi.fn(),
    startLogin: vi.fn(),
    completeLogin: vi.fn(),
    resendLoginCode: vi.fn(),
    requestEmailVerification: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    capture: vi.fn(() => ({
      generation: state.value.generation,
      subjectId: state.value.subjectId,
    })),
    readProfile: vi.fn().mockResolvedValue(profile()),
    updateProfile: vi.fn().mockResolvedValue(profile({ version: 8n })),
    readSettings: vi.fn(),
    updateSettings: vi.fn(),
    readAddresses: vi.fn(),
    createAddress: vi.fn(),
    updateAddress: vi.fn(),
    deleteAddress: vi.fn(),
    setDefaultAddress: vi.fn(),
    dispose: vi.fn(),
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
async function open(session: SessionController, path = '/account/id') {
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push(path);
  await router.isReady();
  const wrapper = mount(
    { template: '<RouterView />' },
    { global: { plugins: [router], provide: { [sessionKey as symbol]: session } } },
  );
  mounted.push(wrapper);
  await flushPromises();
  return { wrapper, router };
}
function button(wrapper: VueWrapper, text: string) {
  const item = wrapper.findAll('button').find((candidate) => candidate.text().includes(text));
  if (!item) throw new Error(`Missing button ${text}`);
  return item;
}

describe('MarketMesh ID', () => {
  it('shows private data, edits with CAS and keeps bio intact', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Вера Ильина');
    expect(wrapper.text()).toContain('12 марта 1989 года');
    expect(wrapper.text()).toContain('Женский');
    expect(wrapper.text()).toContain('Вы с нами с');
    expect(wrapper.find('.id-email').text()).toBe('vera@example.comПодтверждён');
    expect(wrapper.find('.privacy-note').text()).toContain(
      'Данные этого раздела доступны только вам.',
    );
    expect(wrapper.find('h1').text()).toBe('MarketMesh ID');
    expect(wrapper.find('#security-title').text()).toBe('Вход и безопасность');
    expect(wrapper.find('#sessions-title').text()).toBe('Сеансы и устройства');
    expect(wrapper.text()).not.toContain('Удаление аккаунта');
    await button(wrapper, 'Изменить данные').trigger('click');
    expect((wrapper.find('#id-first').element as HTMLInputElement).value).toBe('Вера');
    await wrapper.find('#id-first').setValue('');
    await wrapper.find('#id-birth').setValue('2999-01-01');
    await wrapper.find('#id-phone').setValue('123');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#id-first-error').text()).toContain('Введите имя');
    expect(wrapper.find('#id-birth-error').text()).toContain('не может быть в будущем');
    expect(wrapper.find('#id-phone-error').text()).toContain('от 7 до 15 цифр');
    expect(session.updateProfile).not.toHaveBeenCalled();
    await wrapper.find('#id-first').setValue('Вера');
    await wrapper.find('#id-birth').setValue('1989-03-12');
    await wrapper.find('#id-phone').setValue('+7 921 000-11-22');
    await wrapper.find('#id-city').setValue('Тверь');
    expect(wrapper.find('#id-city-error').exists()).toBe(false);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(session.updateProfile).toHaveBeenCalledWith(
      {
        displayName: 'Вера',
        bio: 'Люблю керамику',
        lastName: 'Ильина',
        birthDate: '1989-03-12',
        gender: Gender.FEMALE,
        phone: '+7 921 000-11-22',
        city: 'Тверь',
        showAge: false,
        expectedVersion: 7n,
      },
      { generation: 'g1', subjectId: '01'.repeat(16) },
    );
    expect(wrapper.find('[role="status"]').text()).toContain('Данные сохранены.');
    expect(wrapper.find('form').exists()).toBe(false);
  });
  it('builds the public signature from name, city and the age toggle', async () => {
    const { session } = fixture();
    vi.mocked(session.updateProfile).mockResolvedValue(profile({ version: 8n, showAge: true }));
    const { wrapper } = await open(session);
    const preview = () => wrapper.find('.review-preview').text();
    expect(preview()).toContain('Вера, Санкт-Петербург');
    expect(preview()).not.toContain('лет');
    const toggle = wrapper.find('.age-choice input');
    expect(toggle.attributes('disabled')).toBeUndefined();
    await toggle.setValue(true);
    await flushPromises();
    expect(session.updateProfile).toHaveBeenCalledWith(
      expect.objectContaining({ showAge: true, expectedVersion: 7n, bio: 'Люблю керамику' }),
      expect.anything(),
    );
    expect(wrapper.find('[role="status"]').text()).toContain('Возраст теперь виден');
    expect(preview()).toMatch(/Вера, Санкт-Петербург, \d+ (лет|года|год)/);
    expect(wrapper.text()).not.toContain('1989-03-12');
  });
  it('disables the age toggle without a birth date', async () => {
    const { session } = fixture();
    vi.mocked(session.readProfile).mockResolvedValue(profile({ birthDate: '' }));
    const { wrapper } = await open(session);
    expect(wrapper.find('.age-choice input').attributes('disabled')).toBeDefined();
    expect(wrapper.text()).toContain('Дата рождения не указана');
  });
  it('keeps the draft through a conflict until reconciliation', async () => {
    const { session } = fixture();
    vi.mocked(session.updateProfile).mockRejectedValueOnce(
      new ConnectError('private', Code.Aborted),
    );
    const { wrapper } = await open(session);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-city').setValue('Мой черновик');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).not.toContain('private');
    expect(wrapper.text()).toContain('изменились в другом окне');
    vi.mocked(session.readProfile).mockResolvedValueOnce(
      profile({ city: 'В другом окне', version: 9n }),
    );
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    expect(wrapper.find('.latest-profile').text()).toContain('В другом окне');
    expect((wrapper.find('#id-city').element as HTMLInputElement).value).toBe('Мой черновик');
    await button(wrapper, 'Оставить мой черновик').trigger('click');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(vi.mocked(session.updateProfile).mock.calls[1]?.[0].expectedVersion).toBe(9n);
  });
  it('clears private data on owner change and requires authentication', async () => {
    const anonymous = fixture('anonymous');
    const view = await open(anonymous.session);
    expect(view.wrapper.text()).toContain('Личное начинается со входа');
    const { session, state } = fixture();
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Вера Ильина');
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Вера Ильина');
    expect(wrapper.text()).not.toContain('Санкт-Петербург');
  });
  it('shows the provisioning state while the profile is pending', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const { session } = fixture('profilePending');
    vi.mocked(session.readProfile).mockRejectedValue(new ConnectError('not yet', Code.NotFound));
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Готовим ваш профиль');
    await button(wrapper, 'Проверить готовность').trigger('click');
    await flushPromises();
    expect(session.readProfile).toHaveBeenCalledTimes(1);
  });
  it('does not retry an unknown mutation and permits explicit acceptance of current server data', async () => {
    const { session } = fixture();
    vi.mocked(session.updateProfile).mockRejectedValue(new TypeError('network private'));
    const { wrapper } = await open(session);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-city').setValue('Новый черновик');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(session.updateProfile).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[role="alert"]').text()).toContain('Сохранение не подтверждено');
    expect(wrapper.text()).not.toContain('network private');
    expect((wrapper.find('#id-city').element as HTMLInputElement).value).toBe('Новый черновик');
    await wrapper.find('form').trigger('submit');
    expect(session.updateProfile).toHaveBeenCalledTimes(1);
    vi.mocked(session.readProfile).mockResolvedValueOnce(
      profile({ city: 'Актуальный город', version: 10n }),
    );
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    await button(wrapper, 'Принять актуальные данные').trigger('click');
    await flushPromises();
    expect(wrapper.find('#id-city').exists()).toBe(false);
    expect(wrapper.find('.id-rows').text()).toContain('Актуальный город');
    expect(session.updateProfile).toHaveBeenCalledTimes(1);
  });
  it('renders server values literally and blocks oversized names', async () => {
    const { session } = fixture();
    vi.mocked(session.readProfile).mockResolvedValue(
      profile({ displayName: '<img src=x onerror=alert(1)>', city: '<script>alert(1)</script>' }),
    );
    const { wrapper } = await open(session);
    expect(wrapper.find('.id-hero h2').text()).toBe('<img src=x onerror=alert(1)> Ильина');
    expect(wrapper.find('img').exists()).toBe(false);
    expect(wrapper.find('script').exists()).toBe(false);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-first').setValue('😀'.repeat(81));
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#id-first').attributes('aria-invalid')).toBe('true');
    expect(wrapper.find('#id-first-error').text()).toContain('80 символов');
    expect(session.updateProfile).not.toHaveBeenCalled();
  });
  it('warns before losing dirty data and forgets the private draft on generation change', async () => {
    const { session, state } = fixture();
    const { wrapper, router } = await open(session);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-city').setValue('Private draft');
    const event = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await router.push('/login');
    expect(confirm).toHaveBeenCalled();
    expect(router.currentRoute.value.path).toBe('/account/id');
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.find('#id-city').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('Private draft');
    expect(wrapper.text()).not.toContain('Вера');
    expect(wrapper.text()).not.toContain('vera@example.com');
    const after = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(after);
    expect(after.defaultPrevented).toBe(false);
  });
  it('discards late profile responses from a previous generation', async () => {
    const { session, state } = fixture();
    let resolve: ((value: Profile) => void) | undefined;
    vi.mocked(session.readProfile).mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const { wrapper } = await open(session);
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    resolve?.(profile({ displayName: 'Private old account' }));
    await flushPromises();
    expect(wrapper.text()).not.toContain('Private old account');
    expect(wrapper.find('.id-rows').exists()).toBe(false);
  });
  it('bounds pending polling, keeps manual retry, and cancels timers on navigation', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const { session } = fixture('profilePending');
    vi.mocked(session.readProfile).mockRejectedValue(new ConnectError('not yet', Code.NotFound));
    const { wrapper, router } = await open(session);
    for (const delay of [1500, 3000, 6000, 12000]) {
      await vi.advanceTimersByTimeAsync(delay);
      await flushPromises();
    }
    expect(session.readProfile).toHaveBeenCalledTimes(4);
    expect(wrapper.text()).toContain('Автоматическая проверка приостановлена');
    await vi.advanceTimersByTimeAsync(60000);
    expect(session.readProfile).toHaveBeenCalledTimes(4);
    await button(wrapper, 'Проверить готовность').trigger('click');
    await flushPromises();
    expect(session.readProfile).toHaveBeenCalledTimes(5);
    await router.push('/login');
    await vi.advanceTimersByTimeAsync(60000);
    expect(session.readProfile).toHaveBeenCalledTimes(5);
  });
  it('does not interpret a generic NotFound as profile provisioning', async () => {
    const { session } = fixture();
    vi.mocked(session.readProfile).mockRejectedValue(
      new ConnectError('PROFILE_NOT_READY', Code.NotFound),
    );
    const { wrapper } = await open(session);
    expect(wrapper.text()).not.toContain('Готовим ваш профиль');
    expect(wrapper.text()).toContain('Данные пока недоступны');
  });
  it('keeps a draft across a same-owner refresh and clears it on unsupported coordination', async () => {
    const { session, state } = fixture();
    const { wrapper } = await open(session);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-city').setValue('Private draft during refresh');
    state.value = { ...state.value, status: 'checking' };
    await flushPromises();
    state.value = { ...state.value, status: 'authenticated' };
    await flushPromises();
    expect((wrapper.find('#id-city').element as HTMLInputElement).value).toBe(
      'Private draft during refresh',
    );
    state.value = { status: 'unsupported', generation: 'g1', subjectId: null };
    await flushPromises();
    state.value = { status: 'authenticated', generation: 'g1', subjectId: '01'.repeat(16) };
    await flushPromises();
    expect(wrapper.find('#id-city').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('Private draft during refresh');
    expect(wrapper.find('.id-rows').text()).toContain('Санкт-Петербург');
  });
  it('clears the previous owner even when recovery reports another subject in the same generation', async () => {
    const { session, state } = fixture();
    const { wrapper } = await open(session);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-city').setValue('Private Vera draft');
    state.value = { status: 'unavailable', generation: 'g1', subjectId: null };
    await flushPromises();
    vi.mocked(session.readProfile).mockResolvedValueOnce(
      profile({ subjectId: new Uint8Array(16).fill(2), displayName: 'Борис', city: 'Тверь' }),
    );
    security.getCredentials.mockResolvedValueOnce({
      email: 'boris@example.com',
      emailVerified: false,
      loginCodeEnabled: false,
      newDeviceCooldownUntilUnix: 0n,
    });
    state.value = { status: 'authenticated', generation: 'g1', subjectId: '02'.repeat(16) };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Вера');
    expect(wrapper.text()).not.toContain('Private Vera draft');
    expect(wrapper.text()).not.toContain('vera@example.com');
    expect(wrapper.find('.id-hero h2').text()).toBe('Борис Ильина');
    expect(wrapper.find('.id-email').text()).toBe('boris@example.comНе подтверждён');
  });
  it('keeps one open form: sign-in changes wait for the personal draft and back', async () => {
    const { session } = fixture();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { wrapper } = await open(session);
    expect(wrapper.findAll('.button.primary')).toHaveLength(0);
    await button(wrapper, 'Изменить данные').trigger('click');
    await wrapper.find('#id-city').setValue('Тверь');
    // Смена пароля, кода и выход везде закрывают сеанс и стёрли бы черновик.
    expect(wrapper.find('#security-locked').text()).toBe(
      'Сначала сохраните или отмените изменения личных данных.',
    );
    for (const name of ['Сменить почту', 'Сменить пароль', 'Включить', 'Выйти на всех']) {
      expect(button(wrapper, name).attributes('disabled')).toBeDefined();
      expect(button(wrapper, name).attributes('aria-describedby')).toContain('security-locked');
    }
    expect(wrapper.findAll('.button.primary')).toHaveLength(1);
    await button(wrapper, 'Отменить').trigger('click');
    await flushPromises();
    expect(wrapper.find('#security-locked').exists()).toBe(false);
    expect(button(wrapper, 'Сменить пароль').attributes('disabled')).toBeUndefined();
    await button(wrapper, 'Сменить пароль').trigger('click');
    await flushPromises();
    const edit = button(wrapper, 'Изменить данные');
    expect(edit.attributes('disabled')).toBeDefined();
    expect(edit.attributes('aria-describedby')).toBe('id-edit-locked');
    expect(wrapper.find('#id-edit-locked').text()).toContain('«Вход и безопасность»');
    expect(wrapper.findAll('.button.primary')).toHaveLength(1);
    await button(wrapper, 'Отменить').trigger('click');
    await flushPromises();
    expect(button(wrapper, 'Изменить данные').attributes('disabled')).toBeUndefined();
    expect(wrapper.find('#id-edit-locked').exists()).toBe(false);
  });
  it('opens the security section from the old security address', async () => {
    const { session } = fixture();
    const { wrapper, router } = await open(session, '/account/security');
    expect(router.currentRoute.value.fullPath).toBe('/account/id#security');
    expect(wrapper.find('#security').exists()).toBe(true);
    expect(wrapper.findAll('.account-nav a').map((link) => link.text())).toEqual(['MarketMesh ID']);
  });
});
