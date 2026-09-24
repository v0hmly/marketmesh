import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Profile } from '../../../shared/api/types';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';
import App from '../../../App.vue';

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
  displayName: 'Алиса',
  bio: 'Люблю керамику',
  version: 7n,
  createdAtUnix: 1n,
  updatedAtUnix: 2n,
  lastName: '',
  birthDate: '',
  gender: 0,
  phone: '',
  city: '',
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
    register: vi.fn().mockResolvedValue(undefined),
    startLogin: vi.fn().mockRejectedValue(new ConnectError('no code step', Code.Unimplemented)),
    completeLogin: vi.fn(),
    resendLoginCode: vi.fn(),
    requestEmailVerification: vi.fn(),
    login: vi.fn().mockResolvedValue(undefined),
    logout: vi.fn().mockResolvedValue(undefined),
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
const credentials = (overrides: Record<string, unknown> = {}) => ({
  email: 'alisa@example.com',
  emailVerified: true,
  loginCodeEnabled: false,
  newDeviceCooldownUntilUnix: 0n,
  ...overrides,
});
const sessionInfo = (id: number, overrides: Record<string, unknown> = {}) => ({
  sessionId: new Uint8Array(16).fill(id),
  device: 'MacBook Air, macOS',
  browser: 'Safari',
  ip: '192.0.2.1',
  location: 'Санкт-Петербург, Россия',
  createdAtUnix: 1790000000n,
  lastSeenAtUnix: 1790003600n,
  current: false,
  ...overrides,
});
beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {});
  security.getCredentials.mockResolvedValue(credentials());
  security.listSessions.mockResolvedValue({
    sessions: [
      sessionInfo(1, { current: true }),
      sessionInfo(2, { device: 'Pixel 8, Android', browser: 'Chrome', location: '' }),
    ],
  });
});
const mounted: VueWrapper[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  vi.useRealTimers();
});
async function open(session: SessionController, path: string) {
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
  const found = wrapper.findAll('button').find((candidate) => candidate.text().includes(text));
  if (!found) throw new Error(`Missing button ${text}`);
  return found;
}
/** ConnectError с ErrorInfo Auth, как его разбирает authErrorReason. */
function authError(code: Code, reason: string) {
  const text = (field: number, value: string) => {
    const bytes = new TextEncoder().encode(value);
    return [(field << 3) | 2, bytes.length, ...bytes];
  };
  const error = new ConnectError('untrusted message', code);
  error.details = [
    {
      type: 'google.rpc.ErrorInfo',
      value: new Uint8Array([...text(1, reason), ...text(2, 'marketmesh.auth')]),
    },
  ];
  return error;
}

describe('account forms', () => {
  it.each(['/login', '/register'])('enforces the password policy on %s', async (path) => {
    const { session } = fixture('anonymous');
    const { wrapper } = await open(session, path);
    const register = path === '/register';
    const emailField = register ? '#reg-email' : '#login-email';
    const passwordField = register ? '#reg-password' : '#login-password';
    await wrapper.find(emailField).setValue('anna@example.ru');
    for (const password of register ? ['1234567', 'abcdefgh'] : ['']) {
      await wrapper.find(passwordField).setValue(password);
      if (register) await wrapper.find('#reg-confirm').setValue(password);
      await wrapper.find('form').trigger('submit');
      expect(
        wrapper.find(register ? '#reg-password-error' : '#login-password-error').exists(),
      ).toBe(true);
      expect(session.login).not.toHaveBeenCalled();
      expect(session.register).not.toHaveBeenCalled();
    }
    if (register) {
      await wrapper.find(passwordField).setValue('Secret123!');
      await wrapper.find('#reg-confirm').setValue('Secret123!');
      await wrapper.find('form').trigger('submit');
      await flushPromises();
      expect(session.register).toHaveBeenCalledTimes(1);
    } else {
      await wrapper.find(passwordField).setValue('long passphrase');
      await wrapper.find('form').trigger('submit');
      await flushPromises();
      expect(session.login).toHaveBeenCalledTimes(1);
    }
  });

  it('shows the password checklist only after typing starts on /register', async () => {
    const { session } = fixture('anonymous');
    const { wrapper } = await open(session, '/register');
    expect(wrapper.find('#reg-password-requirements').exists()).toBe(false);
    await wrapper.find('#reg-password').setValue('Secret1');
    expect(wrapper.find('#reg-password-requirements').exists()).toBe(true);
    expect(wrapper.findAll('.password-requirements li')).toHaveLength(5);
    expect(wrapper.findAll('.password-requirements li.requirement-met').length).toBeGreaterThan(0);
    expect(wrapper.find('#reg-password-requirements').text()).toContain('От 8 до 64 символов');
  });

  it.each(['/login', '/register'])(
    'blocks credentials until delayed bootstrap settles on %s',
    async (path) => {
      const { session, state } = fixture('unknown');
      let release!: () => void;
      const gate = new Promise<void>((resolve) => {
        release = resolve;
      });
      vi.mocked(session.bootstrap).mockImplementation(async () => {
        await gate;
        state.value = { status: 'checking', generation: 'g2', subjectId: null };
        await Promise.resolve();
        state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
      });
      const router = createStorefrontRouter(createMemoryHistory());
      await router.push(path);
      await router.isReady();
      const wrapper = mount(App, {
        global: { plugins: [router], provide: { [sessionKey as symbol]: session } },
      });
      mounted.push(wrapper);
      await flushPromises();
      expect(session.bootstrap).toHaveBeenCalledTimes(1);
      const register = path === '/register';
      const emailField = register ? '#reg-email' : '#login-email';
      const passwordField = register ? '#reg-password' : '#login-password';
      expect(wrapper.find(emailField).attributes('disabled')).toBeDefined();
      expect(wrapper.find(passwordField).attributes('disabled')).toBeDefined();
      await wrapper.find('form').trigger('submit');
      expect(session.login).not.toHaveBeenCalled();
      expect(session.register).not.toHaveBeenCalled();
      release();
      await flushPromises();
      expect(wrapper.find(emailField).attributes('disabled')).toBeUndefined();
      await wrapper.find(emailField).setValue('anna@example.ru');
      await wrapper.find(passwordField).setValue(register ? 'Secret123!' : 'long passphrase');
      if (register) await wrapper.find('#reg-confirm').setValue('Secret123!');
      await wrapper.find('form').trigger('submit');
      await flushPromises();
      expect(path === '/login' ? session.login : session.register).toHaveBeenCalledTimes(1);
      expect(
        vi.mocked(path === '/login' ? session.login : session.register).mock.calls[0]?.[0],
      ).toBe('anna@example.ru');
    },
  );

  it('labels credentials, clears passwords after sending, and shows the verification card', async () => {
    const { session } = fixture('anonymous');
    let bytes: Uint8Array | undefined;
    vi.mocked(session.register).mockImplementation(async (_identifier, password) => {
      bytes = password;
      expect(new TextDecoder().decode(password)).toBe('Secret123!');
    });
    vi.mocked(session.requestEmailVerification).mockResolvedValue(undefined);
    const { wrapper } = await open(session, '/register');
    expect(wrapper.find('label[for="reg-email"]').text()).toBe('Почта');
    expect(wrapper.find('#reg-email').attributes('autocomplete')).toBe('username');
    expect(wrapper.find('label[for="reg-password"]').text()).toBe('Пароль');
    expect(wrapper.find('#reg-password').attributes('autocomplete')).toBe('new-password');
    await wrapper.find('#reg-email').setValue('anna@example.ru');
    await wrapper.find('#reg-password').setValue('Secret123!');
    await wrapper.find('#reg-confirm').setValue('Secret123!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Аккаунт создан.');
    expect(wrapper.text()).toContain('Мы отправили письмо для подтверждения на anna@example.ru');
    expect(bytes?.every((value) => value === 0)).toBe(true);
    await button(wrapper, 'Отправить письмо ещё раз').trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('Письмо отправлено повторно.');
  });

  it('keeps the neutral registration outcome when the backend cannot email yet', async () => {
    const { session } = fixture('anonymous');
    vi.mocked(session.requestEmailVerification).mockRejectedValue(
      new ConnectError('no mail', Code.Unimplemented),
    );
    const { wrapper } = await open(session, '/register');
    await wrapper.find('#reg-email').setValue('anna@example.ru');
    await wrapper.find('#reg-password').setValue('Secret123!');
    await wrapper.find('#reg-confirm').setValue('Secret123!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Запрос обработан. Теперь войдите с вашей почтой и паролем.');
    expect(wrapper.text()).not.toContain('Мы отправили письмо');
  });

  it('never displays raw login diagnostics and keeps the draft password on failure', async () => {
    const { session } = fixture('anonymous');
    vi.mocked(session.login).mockRejectedValue(
      new ConnectError('internal secret db-host', Code.Unauthenticated),
    );
    const { wrapper } = await open(session, '/login');
    await wrapper.find('#login-email').setValue('anna@example.ru');
    await wrapper.find('#login-password').setValue('long passphrase');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain(
      'Почта или пароль указаны неверно. Проверьте данные и попробуйте снова.',
    );
    expect(wrapper.text()).not.toContain('db-host');
    expect((wrapper.find('#login-password').element as HTMLInputElement).value).toBe(
      'long passphrase',
    );
  });

  it('locks the form after five failed attempts', async () => {
    const { session } = fixture('anonymous');
    vi.mocked(session.login).mockRejectedValue(new ConnectError('no', Code.Unauthenticated));
    const { wrapper } = await open(session, '/login');
    await wrapper.find('#login-email').setValue('anna@example.ru');
    await wrapper.find('#login-password').setValue('long passphrase');
    for (let attempt = 0; attempt < 5; attempt++) {
      await wrapper.find('form').trigger('submit');
      await flushPromises();
    }
    expect(wrapper.find('[role="alert"]').text()).toContain('Вход временно заблокирован');
    expect(wrapper.find('#login-email').attributes('disabled')).toBeDefined();
    expect(button(wrapper, 'Вход заблокирован').attributes('disabled')).toBeDefined();
  });

  it('walks through the code step with resend and wrong-code handling', async () => {
    const { session } = fixture('anonymous');
    const challenge = { challengeId: new Uint8Array(16).fill(9), codeExpiresInSeconds: 600n };
    vi.mocked(session.startLogin).mockResolvedValue(challenge);
    vi.mocked(session.resendLoginCode).mockResolvedValue(challenge);
    vi.mocked(session.completeLogin).mockRejectedValueOnce(
      new ConnectError('no', Code.InvalidArgument, {
        /* без ErrorInfo — обычная сетевая ошибка классифицируется иначе */
      }),
    );
    const { wrapper, router } = await open(session, '/login');
    await wrapper.find('#login-email').setValue('anna@example.ru');
    await wrapper.find('#login-password').setValue('long passphrase');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('h1').text()).toBe('Подтвердите вход.');
    expect(wrapper.find('#login-code-help').text()).toContain('10 минут');
    const codeInput = wrapper.find('#login-code');
    await codeInput.setValue('48a29');
    expect((codeInput.element as HTMLInputElement).value).toBe('4829');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#login-code-error').text()).toBe('Код состоит из шести цифр.');
    expect(session.completeLogin).not.toHaveBeenCalled();
    await button(wrapper, 'Отправить код ещё раз').trigger('click');
    await flushPromises();
    expect(session.resendLoginCode).toHaveBeenCalledWith(challenge);
    expect(wrapper.text()).toContain('Новый код отправлен на почту.');
    await codeInput.setValue('482913');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('Не удалось проверить код');
    vi.mocked(session.completeLogin).mockResolvedValue(undefined);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    // Без MarketMesh ID и заказов первый раздел кабинета — вход и безопасность.
    expect(router.currentRoute.value.path).toBe('/account/security');
  });
});

describe('cabinet without MarketMesh ID', () => {
  it('lands on sign-in security and keeps it as the only section', async () => {
    const { session } = fixture();
    const { wrapper, router } = await open(session, '/account');
    expect(router.currentRoute.value.path).toBe('/account/security');
    expect(wrapper.findAll('.account-nav a').map((link) => link.text())).toEqual([
      'Вход и безопасность',
    ]);
    expect(wrapper.find('.account-nav a').attributes('aria-current')).toBe('page');
    expect(wrapper.find('h1').text()).toBe('Вход и безопасность');
    expect(wrapper.find('#theme-pick').exists()).toBe(false);
    expect(wrapper.text()).toContain('alisa@example.com');
    await router.push('/account/settings');
    expect(router.currentRoute.value.path).toBe('/account/security');
  });
  it('refreshes an expired session once before rereading sign-in data', async () => {
    const { session } = fixture();
    security.getCredentials.mockRejectedValueOnce(
      new ConnectError('expired', Code.Unauthenticated),
    );
    const { wrapper } = await open(session, '/account/security');
    expect(session.bootstrap).toHaveBeenCalledTimes(1);
    expect(security.getCredentials).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain('alisa@example.com');
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
  });
  it('shows sessions with device, place and the current mark, and ends another one', async () => {
    const { session } = fixture();
    security.revokeSession.mockResolvedValue({});
    const { wrapper } = await open(session, '/account/security');
    const rows = wrapper.findAll('.session-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('MacBook Air, macOS, Safari');
    expect(rows[0]!.text()).toContain('Текущий сеанс');
    expect(rows[0]!.text()).toContain('Санкт-Петербург, Россия');
    expect(rows[0]!.find('button').exists()).toBe(false);
    expect(rows[1]!.text()).toContain('Место не определено');
    expect(wrapper.text()).toContain('Город определяется по IP и может быть неточным.');
    await rows[1]!.find('button').trigger('click');
    await flushPromises();
    expect(security.revokeSession).toHaveBeenCalledWith({
      sessionId: new Uint8Array(16).fill(2),
    });
    expect(session.endSession).not.toHaveBeenCalled();
    expect(wrapper.find('[role="status"]').text()).toContain('Pixel 8, Android, Chrome');
    expect(security.listSessions).toHaveBeenCalledTimes(2);
  });
  it('says honestly that signing out everywhere also ends this session', async () => {
    const { session, state } = fixture();
    security.logoutAll.mockImplementation(async () => {
      state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
      return {};
    });
    const { wrapper } = await open(session, '/account/security');
    expect(wrapper.find('#logout-all-help').text()).toContain('включая этот');
    await button(wrapper, 'Выйти на всех устройствах').trigger('click');
    await flushPromises();
    expect(session.endSession).toHaveBeenCalledTimes(1);
    expect(security.logoutAll).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('Все сеансы закрыты, включая этот.');
    expect(wrapper.text()).not.toContain('alisa@example.com');
    expect(wrapper.find('.account-sidebar').exists()).toBe(false);
  });
  it('blocks ending other sessions during the new-device cooldown', async () => {
    const { session } = fixture();
    security.getCredentials.mockResolvedValue(
      credentials({ newDeviceCooldownUntilUnix: BigInt(Math.floor(Date.now() / 1000) + 3600) }),
    );
    const { wrapper } = await open(session, '/account/security');
    // Auth считает защиту от входа в этом сеансе, а не от нового устройства.
    expect(wrapper.find('#sessions-cooldown').text()).toContain(
      'Вход в этом сеансе выполнен меньше суток назад.',
    );
    expect(wrapper.text()).not.toContain('нового устройства');
    expect(wrapper.find('#sessions-cooldown').classes()).not.toContain('error');
    const revoke = wrapper.findAll('.session-row')[1]!.find('button');
    expect(revoke.attributes('disabled')).toBeDefined();
    expect(revoke.attributes('aria-describedby')).toBe('sessions-cooldown');
    const everywhere = button(wrapper, 'Выйти на всех устройствах');
    expect(everywhere.attributes('disabled')).toBeDefined();
    expect(everywhere.attributes('aria-describedby')).toBe('logout-all-help sessions-cooldown');
  });
  it('opens one form at a time with a single primary action and wipes secrets on switch', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session, '/account/security');
    expect(wrapper.findAll('.security-sections .button.primary')).toHaveLength(0);
    expect(button(wrapper, 'Сменить почту').classes()).toContain('secondary');
    expect(wrapper.find('fieldset').exists()).toBe(false);
    await button(wrapper, 'Сменить пароль').trigger('click');
    await wrapper.find('#security-password-first').setValue('old secret');
    expect(wrapper.findAll('.security-sections .button.primary')).toHaveLength(1);
    await button(wrapper, 'Сменить почту').trigger('click');
    expect(wrapper.find('#security-password-first').exists()).toBe(false);
    expect(wrapper.findAll('.security-sections .button.primary')).toHaveLength(1);
    await button(wrapper, 'Сменить пароль').trigger('click');
    expect((wrapper.find('#security-password-first').element as HTMLInputElement).value).toBe('');
    expect(wrapper.text()).not.toContain('Обновить данные');
    expect(wrapper.findAll('h2').map((heading) => heading.text())).not.toContainEqual(
      expect.stringMatching(/\.$/),
    );
  });
  it('starts an email change with the current password and closes the form', async () => {
    const { session } = fixture();
    security.startEmailChange.mockResolvedValue({});
    const { wrapper } = await open(session, '/account/security');
    await button(wrapper, 'Сменить почту').trigger('click');
    await wrapper.find('#security-email-first').setValue('new@example.com');
    await wrapper.find('#email-password').setValue('Secret123!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(security.startEmailChange).toHaveBeenCalledTimes(1);
    const request = security.startEmailChange.mock.calls[0]![0];
    expect(request.newEmail).toBe('new@example.com');
    // Пароль передан байтами и стёрт сразу после запроса.
    expect(request.password).toHaveLength('Secret123!'.length);
    expect(Array.from(request.password as Uint8Array).every((byte) => byte === 0)).toBe(true);
    expect(wrapper.find('#security-email-first').exists()).toBe(false);
    expect(wrapper.find('[role="status"]').text()).toContain('Письмо подтверждения отправлено');
  });
  it('changes the password through the session end and wipes both passwords', async () => {
    const { session, state } = fixture();
    security.changePassword.mockImplementation(async () => {
      state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
      return {};
    });
    const { wrapper } = await open(session, '/account/security');
    await button(wrapper, 'Сменить пароль').trigger('click');
    await wrapper.find('#security-password-first').setValue('OldSecret1!');
    await wrapper.find('#new-password').setValue('NewSecret2!');
    await wrapper.find('#repeat-password').setValue('NewSecret2!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(session.endSession).toHaveBeenCalledTimes(1);
    const request = security.changePassword.mock.calls[0]![0];
    expect(request.currentPassword).toHaveLength('OldSecret1!'.length);
    expect(Array.from(request.currentPassword as Uint8Array).every((byte) => byte === 0)).toBe(
      true,
    );
    expect(Array.from(request.newPassword as Uint8Array).every((byte) => byte === 0)).toBe(true);
    expect(wrapper.text()).toContain('Пароль изменён. Все сеансы закрыты.');
  });
  it('keeps the rejection visible after a wrong current password restores the session', async () => {
    const { session, state } = fixture();
    vi.mocked(session.endSession).mockImplementation(async (action) => {
      state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
      try {
        return await action();
      } finally {
        state.value = { status: 'authenticated', generation: 'g3', subjectId: '01'.repeat(16) };
      }
    });
    security.changePassword.mockRejectedValue(new ConnectError('wrong', Code.Unauthenticated));
    const { wrapper } = await open(session, '/account/security');
    await button(wrapper, 'Сменить пароль').trigger('click');
    await wrapper.find('#security-password-first').setValue('WrongSecret1!');
    await wrapper.find('#new-password').setValue('NewSecret2!');
    await wrapper.find('#repeat-password').setValue('NewSecret2!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('Сеанс или пароль не подтверждён.');
    // Сеанс восстановлен под новым поколением: данные перечитаны, форма закрыта и пуста.
    expect(security.getCredentials).toHaveBeenCalledTimes(2);
    expect(wrapper.find('#security-password-first').exists()).toBe(false);
    expect(wrapper.text()).toContain('alisa@example.com');
  });
  it('switches the login code with an emailed code and keeps the challenge after a wrong code', async () => {
    const { session } = fixture();
    const challengeId = new Uint8Array(16).fill(5);
    security.startLoginCodeChange.mockResolvedValue({ challengeId, codeExpiresInSeconds: 600n });
    security.completeLoginCodeChange
      .mockRejectedValueOnce(authError(Code.InvalidArgument, 'CODE_MISMATCH'))
      .mockRejectedValueOnce(authError(Code.FailedPrecondition, 'CODE_REISSUED'))
      .mockResolvedValueOnce({});
    const { wrapper } = await open(session, '/account/security');
    await button(wrapper, 'Включить').trigger('click');
    await wrapper.find('#security-code-first').setValue('Secret123!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(security.startLoginCodeChange.mock.calls[0]![0].enabled).toBe(true);
    expect(wrapper.text()).toContain('Код отправлен на вашу почту.');
    // Пока код не подтверждён, почту и пароль не сменить — и это сказано рядом.
    for (const name of ['Сменить почту', 'Сменить пароль']) {
      expect(button(wrapper, name).attributes('disabled')).toBeDefined();
      expect(button(wrapper, name).attributes('aria-describedby')).toBe('security-code-pending');
    }
    expect(wrapper.find('#security-code-pending').exists()).toBe(true);
    for (const reply of ['111111', '222222']) {
      await wrapper.find('#security-code').setValue(reply);
      await wrapper.find('form').trigger('submit');
      await flushPromises();
      expect(wrapper.find('#security-code').exists()).toBe(true);
      expect(wrapper.find('[role="alert"]').exists()).toBe(true);
    }
    await wrapper.find('#security-code').setValue('333333');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(security.completeLoginCodeChange).toHaveBeenCalledTimes(3);
    expect(security.completeLoginCodeChange.mock.calls[2]![0]).toMatchObject({
      challengeId,
      enabled: true,
      code: '333333',
    });
    expect(session.endSession).toHaveBeenCalledTimes(3);
    expect(wrapper.text()).toContain('Настройка входа изменена.');
  });
  it('cancels a pending code change and frees the other forms', async () => {
    const { session } = fixture();
    security.startLoginCodeChange.mockResolvedValue({
      challengeId: new Uint8Array(16).fill(5),
      codeExpiresInSeconds: 600n,
    });
    const { wrapper } = await open(session, '/account/security');
    document.body.append(wrapper.element);
    await button(wrapper, 'Включить').trigger('click');
    await wrapper.find('#security-code-first').setValue('Secret123!');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    await wrapper.find('#security-code').setValue('12');
    const cancel = wrapper
      .findAll('form button')
      .find((candidate) => candidate.text() === 'Отменить');
    await cancel!.trigger('click');
    await flushPromises();
    expect(wrapper.find('#security-code').exists()).toBe(false);
    expect(button(wrapper, 'Сменить почту').attributes('disabled')).toBeUndefined();
    expect(button(wrapper, 'Сменить пароль').attributes('disabled')).toBeUndefined();
    expect(document.activeElement?.id).toBe('security-code-toggle');
    expect(security.completeLoginCodeChange).not.toHaveBeenCalled();
    wrapper.element.remove();
  });
  it('reports a failed session end without rereading the list over the message', async () => {
    const { session } = fixture();
    security.revokeSession.mockRejectedValue(new ConnectError('gone', Code.NotFound));
    const { wrapper } = await open(session, '/account/security');
    await wrapper.findAll('.session-row')[1]!.find('button').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('Запись уже недоступна.');
    expect(security.listSessions).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll('.session-row')).toHaveLength(2);
  });
  it('shows the provisioning state while the profile is pending', async () => {
    const { session } = fixture('profilePending');
    const { wrapper } = await open(session, '/account/security');
    expect(wrapper.text()).toContain('Готовим ваш аккаунт');
    expect(security.getCredentials).not.toHaveBeenCalled();
    await button(wrapper, 'Проверить готовность').trigger('click');
    await flushPromises();
    expect(session.readProfile).toHaveBeenCalledTimes(1);
  });
});
