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
  ordersEnabled: false,
  favoritesEnabled: true,
  reviewsEnabled: false,
  idEnabled: false,
  sellerEnabled: false,
}));
vi.mock('../sample-data', async (importOriginal) => {
  const original = await importOriginal<typeof import('../sample-data')>();
  return { ...original, loadSampleFavorites: vi.fn(original.loadSampleFavorites) };
});
import { loadSampleFavorites } from '../sample-data';

function fixture(initial: SessionState['status'] = 'authenticated') {
  const state = shallowRef<SessionState>({
    status: initial,
    generation: 'g1',
    subjectId: initial === 'authenticated' ? '01'.repeat(16) : null,
  });
  const session: SessionController = {
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
  await router.push('/account/favorites');
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

describe('favorites section', () => {
  it('lists sample favorites, filters by stock and removes an item', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    expect(wrapper.findAll('.favorite-card')).toHaveLength(6);
    expect(wrapper.text()).toContain('6 изделий в избранном');
    await button(wrapper, 'Закончились').trigger('click');
    expect(wrapper.findAll('.favorite-card')).toHaveLength(2);
    await button(wrapper, 'Сообщить о пополнении: Свеча «Хвоя и дым», 200 мл').trigger('click');
    expect(wrapper.find('[role="status"]').text()).toContain('когда мастер пополнит партию');
    expect(wrapper.text()).toContain('Сообщим о пополнении');
    await button(wrapper, 'Все').trigger('click');
    await button(wrapper, 'Убрать из избранного: Набор открыток «Север»').trigger('click');
    expect(wrapper.findAll('.favorite-card')).toHaveLength(5);
    expect(wrapper.find('[role="status"]').text()).toContain('убрано из избранного');
    expect(wrapper.text()).toContain('5 изделий в избранном');
  });
  it('shows per-filter empty states', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    await button(wrapper, 'В наличии').trigger('click');
    expect(wrapper.findAll('.favorite-card')).toHaveLength(4);
    await button(wrapper, 'Убрать из избранного: Плед из шерсти мериноса').trigger('click');
    await button(wrapper, 'Убрать из избранного: Керамическая тарелка «Волна»').trigger('click');
    await button(wrapper, 'Убрать из избранного: Льняная скатерть, 140 × 220').trigger('click');
    await button(wrapper, 'Убрать из избранного: Набор открыток «Север»').trigger('click');
    expect(wrapper.text()).toContain('Ничего нет в наличии');
  });
  it('surfaces a load failure without inventing data and retries on demand', async () => {
    const { session } = fixture();
    vi.mocked(loadSampleFavorites).mockRejectedValueOnce(new Error('private offline'));
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Избранное пока недоступно');
    expect(wrapper.text()).not.toContain('private offline');
    await button(wrapper, 'Повторить загрузку').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('.favorite-card')).toHaveLength(6);
  });
  it('requires authentication and clears the list on logout', async () => {
    const anonymous = fixture('anonymous');
    const view = await open(anonymous.session);
    expect(view.wrapper.text()).toContain('Личное начинается со входа');
    expect(view.wrapper.findAll('.favorite-card')).toHaveLength(0);
    const { session, state } = fixture();
    const { wrapper } = await open(session);
    state.value = { status: 'signingOut', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Плед из шерсти мериноса');
  });
});
