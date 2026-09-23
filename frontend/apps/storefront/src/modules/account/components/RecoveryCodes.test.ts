import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { sessionKey } from '../../../shell/context';
import type { SessionController, SessionState } from '../../../shell/session';
import RecoveryCodes from './RecoveryCodes.vue';

const api = vi.hoisted(() => ({ startRecoveryCodes: vi.fn(), completeRecoveryCodes: vi.fn() }));
vi.mock('../security/api', async (original) => ({
  ...(await original<object>()),
  createSecurityApi: () => api,
}));
const secretSet = () =>
  Array.from(
    { length: 8 },
    (_, index) => `${index.toString().padStart(8, '0')}-00000000-00000000-00000000`,
  );
const mounted: VueWrapper[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  vi.clearAllMocks();
});
function fixture() {
  api.startRecoveryCodes.mockResolvedValue({
    challengeId: new Uint8Array(16).fill(1),
    codeExpiresInSeconds: 600n,
  });
  api.completeRecoveryCodes.mockResolvedValue({ codes: secretSet() });
  const state = shallowRef<SessionState>({
    status: 'authenticated',
    generation: 'one',
    subjectId: '01'.repeat(16),
  });
  const session = {
    state,
    capture: () => ({ generation: state.value.generation, subjectId: state.value.subjectId }),
    withSession: vi.fn(async (_guard, action: () => Promise<unknown>) => action()),
  } as unknown as SessionController;
  const wrapper = mount(RecoveryCodes, {
    props: { remaining: 0, disabled: false },
    global: { provide: { [sessionKey as symbol]: session } },
  });
  mounted.push(wrapper);
  return { wrapper, state };
}
async function start(wrapper: VueWrapper) {
  await wrapper.get('#recovery-password').setValue('test password');
  await wrapper.get('form').trigger('submit');
  await flushPromises();
  expect(
    api.startRecoveryCodes.mock.calls[0]![0].password.every((byte: number) => byte === 0),
  ).toBe(true);
}
async function confirm(wrapper: VueWrapper) {
  await wrapper.get('#recovery-email-code').setValue('123456');
  await wrapper.get('form').trigger('submit');
  await flushPromises();
}
describe('single-display recovery secrets', () => {
  it('shows the new set once and erases it on explicit hide', async () => {
    const { wrapper } = fixture();
    await start(wrapper);
    await confirm(wrapper);
    expect(wrapper.findAll('code').map((item) => item.text())).toEqual(secretSet());
    expect(wrapper.emitted('generated')).toEqual([[8]]);
    await wrapper.get('button').trigger('click');
    expect(wrapper.findAll('code')).toHaveLength(0);
    expect(wrapper.get('#recovery-password').element).toHaveProperty('value', '');
    expect(api.completeRecoveryCodes).toHaveBeenCalledTimes(1);
  });
  it('discards a delayed secret response after an identity change', async () => {
    const { wrapper, state } = fixture();
    let finish!: (result: { codes: string[] }) => void;
    api.completeRecoveryCodes.mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    await start(wrapper);
    await confirm(wrapper);
    state.value = { status: 'authenticated', generation: 'two', subjectId: '02'.repeat(16) };
    const result = { codes: secretSet() };
    finish(result);
    await flushPromises();
    expect(wrapper.findAll('code')).toHaveLength(0);
    expect(result.codes.every((value) => value === '')).toBe(true);
    expect(wrapper.emitted('generated')).toBeUndefined();
  });
  it('does not replay generation after a lost response and requires a new proof', async () => {
    const { wrapper } = fixture();
    api.completeRecoveryCodes.mockRejectedValue(new TypeError('network lost'));
    await start(wrapper);
    await confirm(wrapper);
    expect(wrapper.get('[role="alert"]').text()).toContain('показать его повторно нельзя');
    expect(wrapper.find('#recovery-email-code').exists()).toBe(false);
    expect(wrapper.find('#recovery-password').exists()).toBe(true);
    expect(api.completeRecoveryCodes).toHaveBeenCalledTimes(1);
  });
  it('erases displayed secrets immediately when the session becomes uncertain', async () => {
    const { wrapper, state } = fixture();
    await start(wrapper);
    await confirm(wrapper);
    state.value = { ...state.value, status: 'uncertain' };
    await flushPromises();
    expect(wrapper.findAll('code')).toHaveLength(0);
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined();
  });
});
