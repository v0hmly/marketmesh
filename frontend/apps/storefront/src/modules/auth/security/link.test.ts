import { Code, ConnectError } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import { sessionKey } from '../../../shell/context';
import SecurityLinkView from '../views/SecurityLinkView.vue';

const api = vi.hoisted(() => ({
  confirmEmail: vi.fn(),
  confirmPasswordReset: vi.fn(),
  confirmEmailChange: vi.fn(),
  requestEmailVerification: vi.fn(),
  requestPasswordReset: vi.fn(),
}));
vi.mock('./api', async (original) => ({
  ...(await original<object>()),
  createSecurityApi: () => api,
}));
const mounted: VueWrapper[] = [];
beforeEach(() => vi.resetAllMocks());
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
});
function expiredError() {
  return new ConnectError('private', Code.FailedPrecondition, undefined, [
    {
      desc: ErrorInfoSchema,
      value: { domain: 'marketmesh.auth', reason: 'TOKEN_EXPIRED' },
    },
  ]);
}
async function open(action: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/account/security/:action', component: SecurityLinkView },
      { path: '/account/security', component: { template: '<p>Security</p>' } },
      { path: '/account', component: { template: '<p>Account</p>' } },
      { path: '/login', component: { template: '<p>Login</p>' } },
    ],
  });
  await router.push(`/account/security/${action}#token=${'a'.repeat(32)}.${'b'.repeat(43)}`);
  await router.isReady();
  const wrapper = mount(SecurityLinkView, {
    global: {
      plugins: [router],
      provide: {
        [sessionKey as symbol]: {
          endSession: async (run: () => Promise<unknown>) => run(),
          confirmEmail: api.confirmEmail,
        },
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  expect(router.currentRoute.value.hash).toBe('');
  return wrapper;
}

describe('registration confirmation', () => {
  it('opens the account after confirmation in the registration browser', async () => {
    api.confirmEmail.mockResolvedValue(true);
    const wrapper = await open('verify');
    expect(api.confirmEmail).not.toHaveBeenCalled();
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(api.confirmEmail).toHaveBeenCalledExactlyOnceWith(`${'a'.repeat(32)}.${'b'.repeat(43)}`);
    expect(wrapper.vm.$router.currentRoute.value.path).toBe('/account');
  });
  it('offers login when confirmation did not establish a session', async () => {
    api.confirmEmail.mockResolvedValue(false);
    const wrapper = await open('verify');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[role="status"]').text()).toContain('Для входа в этом браузере');
    expect(wrapper.vm.$router.currentRoute.value.path).toBe('/account/security/verify');
  });
});

describe('expired security links', () => {
  it.each(['verify', 'reset'])(
    'offers an explicit new %s request, without automatic sending',
    async (action) => {
      api.confirmEmail.mockRejectedValue(expiredError());
      api.confirmPasswordReset.mockRejectedValue(expiredError());
      const wrapper = await open(action);
      if (action === 'reset') {
        await wrapper.get('#reset-password').setValue('CorrectHorse9!');
        await wrapper.get('#reset-repeat').setValue('CorrectHorse9!');
      }
      await wrapper.get('form').trigger('submit');
      await flushPromises();
      expect(wrapper.get('[role="alert"]').text()).toContain('срок её действия истёк');
      expect(wrapper.find('#reset-password').exists()).toBe(false);
      expect(api.requestEmailVerification).not.toHaveBeenCalled();
      expect(api.requestPasswordReset).not.toHaveBeenCalled();
      await wrapper.get('button').trigger('click');
      expect(wrapper.find('[role="alert"]').exists()).toBe(false);
      expect(api.requestEmailVerification).not.toHaveBeenCalled();
      expect(api.requestPasswordReset).not.toHaveBeenCalled();
      await wrapper.get('#link-email').setValue('buyer@example.test');
      await wrapper.get('form').trigger('submit');
      await flushPromises();
      expect(
        action === 'verify' ? api.requestEmailVerification : api.requestPasswordReset,
      ).toHaveBeenCalledExactlyOnceWith({ email: 'buyer@example.test' });
      expect(wrapper.get('[role="status"]').text()).toContain('Если для этой почты');
    },
  );

  it('does not classify unknown network errors as expired links', async () => {
    api.confirmEmail.mockRejectedValue(new TypeError('private network detail'));
    const wrapper = await open('verify');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).not.toContain('Получить новую ссылку');
    expect(wrapper.text()).not.toContain('private network detail');
    expect(api.requestEmailVerification).not.toHaveBeenCalled();
  });

  it('routes an expired email-change link back to security settings', async () => {
    api.confirmEmailChange.mockRejectedValue(expiredError());
    const wrapper = await open('change_email');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('a[href="/account/security"]').text()).toContain('настройкам безопасности');
    expect(wrapper.text()).not.toContain('Получить новую ссылку');
  });
});
