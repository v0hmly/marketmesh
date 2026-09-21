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
  addressesEnabled: false,
  settingsEnabled: false,
  ordersEnabled: false,
  favoritesEnabled: false,
  reviewsEnabled: false,
  idEnabled: true,
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
beforeEach(() => vi.spyOn(window, 'scrollTo').mockImplementation(() => {}));
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  vi.useRealTimers();
});
async function open(session: SessionController) {
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push('/account/id');
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
    expect(wrapper.text()).not.toContain('Сеансы и устройства');
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
});
