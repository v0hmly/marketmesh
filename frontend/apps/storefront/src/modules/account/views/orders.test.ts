import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';
vi.mock('../../../shared/features', () => ({
  addressesEnabled: false,
  settingsEnabled: false,
  ordersEnabled: true,
  favoritesEnabled: false,
  reviewsEnabled: false,
  idEnabled: false,
  sellerEnabled: false,
  staffEnabled: false,
}));
vi.mock('../sample-data', async (importOriginal) => {
  const original = await importOriginal<typeof import('../sample-data')>();
  return { ...original, loadSampleOrders: vi.fn(original.loadSampleOrders) };
});
import { loadSampleOrders } from '../sample-data';

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
    readProfile: vi.fn(),
    updateProfile: vi.fn(),
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
beforeEach(() => vi.spyOn(window, 'scrollTo').mockImplementation(() => {}));
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
});
async function open(session: SessionController) {
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push('/account/orders');
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

describe('orders section', () => {
  it('lists sample orders, filters by status and reveals the pickup code', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    expect(wrapper.findAll('.sample-card')).toHaveLength(4);
    expect(wrapper.text()).toContain('Заказ № 1482-0091');
    expect(wrapper.text()).not.toContain('481 902');
    await button(wrapper, 'Показать код').trigger('click');
    expect(wrapper.text()).toContain('481 902');
    expect(wrapper.find('[role="status"]').text()).toContain('Покажите код сотруднику');
    await button(wrapper, 'Готовы к получению').trigger('click');
    expect(wrapper.findAll('.sample-card')).toHaveLength(1);
    await button(wrapper, 'Отменённые').trigger('click');
    expect(wrapper.text()).toContain('Заказ № 1388-0064');
    await button(wrapper, 'В пути').trigger('click');
    expect(wrapper.find('.order-steps').exists()).toBe(true);
    expect(wrapper.findAll('.order-steps .step-done').length).toBeGreaterThan(0);
  });
  it('shows the empty text of the active filter', async () => {
    const { session } = fixture();
    vi.mocked(loadSampleOrders).mockResolvedValueOnce([]);
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Заказов пока нет.');
    await button(wrapper, 'Отменённые').trigger('click');
    expect(wrapper.text()).toContain('Отменённых заказов нет.');
  });
  it('surfaces a load failure without inventing data and retries on demand', async () => {
    const { session } = fixture();
    vi.mocked(loadSampleOrders).mockRejectedValueOnce(new Error('private offline'));
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Заказы пока недоступны');
    expect(wrapper.text()).toContain('Не удалось загрузить заказы');
    expect(wrapper.text()).not.toContain('private offline');
    await button(wrapper, 'Повторить загрузку').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('.sample-card')).toHaveLength(4);
  });
  it('requires authentication and clears the list on logout', async () => {
    const anonymous = fixture('anonymous');
    const view = await open(anonymous.session);
    expect(view.wrapper.text()).toContain('Личное начинается со входа');
    expect(view.wrapper.findAll('.sample-card')).toHaveLength(0);
    const { session, state } = fixture();
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Заказ № 1482-0091');
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Заказ № 1482-0091');
  });
});
