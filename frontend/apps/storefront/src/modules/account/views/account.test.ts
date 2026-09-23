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
beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {});
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
    expect(router.currentRoute.value.path).toBe('/account');
  });

  it('accepts a recovery code only after the password-approved challenge and never retries it', async () => {
    const { session } = fixture('anonymous');
    const challenge = { challengeId: new Uint8Array(16).fill(9), codeExpiresInSeconds: 600n };
    vi.mocked(session.startLogin).mockResolvedValue(challenge);
    vi.mocked(session.completeLogin).mockRejectedValue(new ConnectError('lost', Code.Unavailable));
    const { wrapper } = await open(session, '/login');
    expect(wrapper.text()).not.toContain('Использовать резервный код');
    await wrapper.find('#login-email').setValue('anna@example.ru');
    await wrapper.find('#login-password').setValue('long passphrase');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    await button(wrapper, 'Использовать резервный код').trigger('click');
    const code = 'abcdef01-23456789-abcdef01-23456789';
    await wrapper.find('#login-code').setValue('  ' + code + '  ');
    expect(wrapper.get('#login-code-help').text()).toContain('резервный код');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(session.completeLogin).toHaveBeenCalledExactlyOnceWith(challenge, code, true);
    expect(wrapper.get('#login-code').element).toHaveProperty('value', '');
    expect(wrapper.get('[role="alert"]').text()).toContain('Код мог быть использован');
  });

  it('keeps a dirty draft and original CAS until explicit conflict reconciliation', async () => {
    const { session } = fixture();
    vi.mocked(session.updateProfile).mockRejectedValueOnce(
      new ConnectError('private', Code.Aborted),
    );
    const { wrapper } = await open(session, '/account');
    expect(wrapper.find('label[for="display-name"]').text()).toBe('Имя');
    expect(wrapper.find('label[for="bio"]').text()).toBe('О себе');
    await wrapper.find('#display-name').setValue('Мой черновик');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(session.updateProfile).toHaveBeenCalledWith(
      {
        displayName: 'Мой черновик',
        bio: 'Люблю керамику',
        expectedVersion: 7n,
        lastName: '',
        birthDate: '',
        gender: 0,
        phone: '',
        city: '',
        showAge: false,
      },
      { generation: 'g1', subjectId: '01'.repeat(16) },
    );
    expect((wrapper.find('#display-name').element as HTMLInputElement).value).toBe('Мой черновик');
    expect(button(wrapper, 'Сохранить изменения').attributes('disabled')).toBeDefined();
    vi.mocked(session.readProfile).mockResolvedValueOnce(
      profile({ displayName: 'В другом окне', version: 9n }),
    );
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    expect(wrapper.find('.latest-profile').text()).toContain('В другом окне');
    expect((wrapper.find('#display-name').element as HTMLInputElement).value).toBe('Мой черновик');
    expect(button(wrapper, 'Сохранить изменения').attributes('disabled')).toBeDefined();
    await button(wrapper, 'Оставить мой черновик').trigger('click');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(vi.mocked(session.updateProfile).mock.calls[1]?.[0].expectedVersion).toBe(9n);
  });

  it('does not retry an unknown mutation and permits explicit acceptance of current server data', async () => {
    const { session } = fixture();
    vi.mocked(session.updateProfile).mockRejectedValue(new TypeError('network private'));
    const { wrapper } = await open(session, '/account');
    await wrapper.find('#bio').setValue('Новый черновик');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(session.updateProfile).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[role="alert"]').text()).toContain('Сохранение не подтверждено');
    expect((wrapper.find('#bio').element as HTMLTextAreaElement).value).toBe('Новый черновик');
    await wrapper.find('form').trigger('submit');
    expect(session.updateProfile).toHaveBeenCalledTimes(1);
    vi.mocked(session.readProfile).mockResolvedValueOnce(
      profile({ bio: 'Актуальный текст', version: 10n }),
    );
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    await button(wrapper, 'Принять актуальный').trigger('click');
    expect((wrapper.find('#bio').element as HTMLTextAreaElement).value).toBe('Актуальный текст');
    expect(button(wrapper, 'Сохранить изменения').attributes('disabled')).toBeDefined();
  });

  it('blocks invalid UTF-8 bounds and renders server values literally', async () => {
    const { session } = fixture();
    vi.mocked(session.readProfile).mockResolvedValue(
      profile({ displayName: '<img src=x onerror=alert(1)>', bio: '<script>alert(1)</script>' }),
    );
    const { wrapper } = await open(session, '/account');
    expect(wrapper.find('.identity-card h2').text()).toBe('<img src=x onerror=alert(1)>');
    expect(wrapper.find('img').exists()).toBe(false);
    expect(wrapper.find('script').exists()).toBe(false);
    await wrapper.find('#display-name').setValue('😀'.repeat(81));
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#display-name').attributes('aria-invalid')).toBe('true');
    expect(wrapper.find('#name-error').text()).toContain('80 символов');
    expect(session.updateProfile).not.toHaveBeenCalled();
  });

  it('warns before losing dirty data and forgets all private draft on generation change', async () => {
    const { session, state } = fixture();
    const { wrapper, router } = await open(session, '/account');
    await wrapper.find('#bio').setValue('Private draft');
    const event = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await router.push('/login');
    expect(confirm).toHaveBeenCalled();
    expect(router.currentRoute.value.path).toBe('/account');
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.find('#bio').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('Private draft');
    expect(wrapper.text()).not.toContain('Алиса');
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
    const { wrapper } = await open(session, '/account');
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    resolve?.(profile({ displayName: 'Private old account' }));
    await flushPromises();
    expect(wrapper.text()).not.toContain('Private old account');
    expect(wrapper.find('#display-name').exists()).toBe(false);
  });

  it('shows pending only from the session state and retries a guarded profile read', async () => {
    const { session } = fixture('profilePending');
    const { wrapper } = await open(session, '/account');
    expect(wrapper.text()).toContain('Готовим ваш профиль');
    expect(session.readProfile).not.toHaveBeenCalled();
    await button(wrapper, 'Проверить готовность').trigger('click');
    await flushPromises();
    expect(session.readProfile).toHaveBeenCalledTimes(1);
  });

  it('bounds pending polling, keeps manual retry, and cancels timers on navigation', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const { session } = fixture('profilePending');
    vi.mocked(session.readProfile).mockRejectedValue(new ConnectError('not yet', Code.NotFound));
    const { wrapper, router } = await open(session, '/account');
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
    const { wrapper } = await open(session, '/account');
    expect(wrapper.text()).not.toContain('Готовим ваш профиль');
    expect(wrapper.text()).toContain('Профиль пока недоступен');
  });

  it('keeps a draft across a same-owner session refresh and clears it on unsupported coordination', async () => {
    const { session, state } = fixture();
    const { wrapper } = await open(session, '/account');
    await wrapper.find('#bio').setValue('Private draft during refresh');
    state.value = { ...state.value, status: 'checking' };
    await flushPromises();
    state.value = { ...state.value, status: 'authenticated' };
    await flushPromises();
    expect((wrapper.find('#bio').element as HTMLTextAreaElement).value).toBe(
      'Private draft during refresh',
    );
    state.value = { status: 'unsupported', generation: 'g1', subjectId: null };
    await flushPromises();
    state.value = { status: 'authenticated', generation: 'g1', subjectId: '01'.repeat(16) };
    await flushPromises();
    expect((wrapper.find('#bio').element as HTMLTextAreaElement).value).toBe('Люблю керамику');
  });

  it('hides private state on logout and never claims that failed revocation succeeded', async () => {
    const { session, state } = fixture();
    const router = createStorefrontRouter(createMemoryHistory());
    await router.push('/account');
    await router.isReady();
    vi.mocked(session.logout).mockImplementation(async () => {
      state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
      await Promise.resolve();
      state.value = { status: 'unavailable', generation: 'g2', subjectId: null };
      throw new ConnectError('private network diagnostic', Code.Unavailable);
    });
    const wrapper = mount(App, {
      global: { plugins: [router], provide: { [sessionKey as symbol]: session } },
    });
    mounted.push(wrapper);
    await flushPromises();
    expect(wrapper.text()).toContain('Алиса');
    await button(wrapper, 'Выйти на всех устройствах').trigger('click');
    await flushPromises();
    expect(session.logout).toHaveBeenCalledWith(true);
    expect(wrapper.text()).not.toContain('Алиса');
    expect(wrapper.text()).toContain('Сервер не подтвердил выход');
    expect(wrapper.text()).not.toContain('private network diagnostic');
  });

  it('clears the previous owner even when recovery reports another subject in the same generation', async () => {
    const { session, state } = fixture();
    const { wrapper } = await open(session, '/account');
    await wrapper.find('#bio').setValue('Private Alice draft');
    state.value = { status: 'unavailable', generation: 'g1', subjectId: null };
    await flushPromises();
    vi.mocked(session.readProfile).mockResolvedValueOnce(
      profile({
        subjectId: new Uint8Array(16).fill(2),
        displayName: 'Борис',
        bio: 'Другой профиль',
      }),
    );
    state.value = { status: 'authenticated', generation: 'g1', subjectId: '02'.repeat(16) };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Алиса');
    expect((wrapper.find('#bio').element as HTMLTextAreaElement).value).toBe('Другой профиль');
    expect(wrapper.text()).toContain('Борис');
  });
});
