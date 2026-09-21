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
  favoritesEnabled: false,
  reviewsEnabled: true,
  idEnabled: false,
  sellerEnabled: false,
}));
vi.mock('../sample-data', async (importOriginal) => {
  const original = await importOriginal<typeof import('../sample-data')>();
  return { ...original, loadSampleReviews: vi.fn(original.loadSampleReviews) };
});
import { loadSampleReviews } from '../sample-data';

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
  await router.push('/account/reviews');
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

describe('reviews section', () => {
  it('validates the review form on submit, then moves the item to my reviews', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Ждут отзыва · 2');
    await button(wrapper, 'Написать отзыв на «Плед из шерсти мериноса»').trigger('click');
    expect(wrapper.find('#rate-w1-1').exists()).toBe(true);
    await wrapper.find('form').trigger('submit');
    expect(wrapper.text()).toContain('Поставьте оценку — без неё отзыв не отправить.');
    expect(wrapper.text()).toContain('Напишите хотя бы одно предложение');
    await wrapper.find('#rate-w1-5').setValue(true);
    expect(wrapper.text()).not.toContain('Поставьте оценку');
    await wrapper.find('#text-w1').setValue('Тёплый и ровный плед, швы аккуратные.');
    expect(wrapper.text()).not.toContain('Напишите хотя бы одно предложение');
    expect(wrapper.find('#count-w1').text()).toContain('/ 2000');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="status"]').text()).toContain(
      'Отзыв отправлен. Мы опубликуем его после модерации',
    );
    expect(wrapper.text()).toContain('Ждут отзыва · 1');
    await button(wrapper, 'Мои отзывы · 4').trigger('click');
    const own = wrapper
      .findAll('.sample-card')
      .find((card) => card.text().includes('Плед из шерсти мериноса'));
    expect(own?.text()).toContain('На модерации');
    expect(own?.find('[role="img"]').attributes('aria-label')).toBe('Оценка 5 из 5');
  });
  it('deletes a published review and reports maker replies', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    await button(wrapper, 'Мои отзывы · 3').trigger('click');
    expect(wrapper.findAll('.sample-card')).toHaveLength(3);
    expect(wrapper.text()).toContain('ОТВЕТ МАСТЕРА');
    await button(wrapper, 'Удалить отзыв на «Свеча «Хвоя и дым», 200 мл»').trigger('click');
    expect(wrapper.findAll('.sample-card')).toHaveLength(2);
    expect(wrapper.find('[role="status"]').text()).toContain('Отзыв удалён.');
  });
  it('shows the empty states of both tabs', async () => {
    const { session } = fixture();
    vi.mocked(loadSampleReviews).mockResolvedValueOnce({ waiting: [], mine: [] });
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Всё рассказано');
    await button(wrapper, 'Мои отзывы · 0').trigger('click');
    expect(wrapper.text()).toContain('Отзывов пока нет');
  });
  it('surfaces a load failure without inventing data and retries on demand', async () => {
    const { session } = fixture();
    vi.mocked(loadSampleReviews).mockRejectedValueOnce(new Error('private offline'));
    const { wrapper } = await open(session);
    expect(wrapper.text()).toContain('Отзывы пока недоступны');
    expect(wrapper.text()).not.toContain('private offline');
    await button(wrapper, 'Повторить загрузку').trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('Ждут отзыва · 2');
  });
  it('requires authentication and clears the draft review on logout', async () => {
    const anonymous = fixture('anonymous');
    const view = await open(anonymous.session);
    expect(view.wrapper.text()).toContain('Личное начинается со входа');
    const { session, state } = fixture();
    const { wrapper } = await open(session);
    await button(wrapper, 'Написать отзыв на «Плед из шерсти мериноса»').trigger('click');
    await wrapper.find('#text-w1').setValue('Приватный черновик отзыва');
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.text()).not.toContain('Приватный черновик отзыва');
    expect(wrapper.find('form').exists()).toBe(false);
  });
});
