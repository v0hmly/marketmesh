import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Code, ConnectError } from '@connectrpc/connect';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import type { AddressBook, Address } from '../../../shared/api/types';
import type { SessionController, SessionState } from '../../../shell/session';
import { sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';
import { emptyAddress } from '../address-validation';
vi.mock('../../../shared/features', () => ({ addressesEnabled: true }));
const addressValue = (value: Omit<Address, '$typeName'>): Address => ({
  $typeName: 'user.v1.Address',
  ...value,
});
const bookValue = (value: Omit<AddressBook, '$typeName'>): AddressBook => ({
  $typeName: 'user.v1.AddressBook',
  ...value,
});
const missingDetail = () => {
  const field = (tag: number, text: string) => {
    const bytes = new TextEncoder().encode(text);
    return [tag, bytes.length, ...bytes];
  };
  return {
    type: 'google.rpc.ErrorInfo',
    value: Uint8Array.from([...field(10, 'ADDRESS_NOT_FOUND'), ...field(18, 'marketmesh.user')]),
  };
};
const fields = (recipient = 'Анна') => ({
  ...emptyAddress(),
  recipient,
  phone: '+7 999 1234567',
  country: 'Россия',
  city: 'Москва',
  streetHouse: 'Улица, дом 1',
});
const address = (id = 1, recipient = 'Анна') =>
  addressValue({
    addressId: new Uint8Array(16).fill(id),
    fields: { $typeName: 'user.v1.AddressFields', ...fields(recipient) },
    isDefault: id === 1,
  });
const snapshot = (version = 1n, addresses = [address()]) =>
  bookValue({ subjectId: new Uint8Array(16).fill(1), version, addresses });
function fixture(initial: SessionState['status'] = 'authenticated') {
  const state = shallowRef<SessionState>({
    status: initial,
    generation: 'g1',
    subjectId: '01'.repeat(16),
  });
  const session: SessionController = {
    state,
    bootstrap: vi.fn(),
    register: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    dispose: vi.fn(),
    capture: vi.fn(() => ({
      generation: state.value.generation,
      subjectId: state.value.subjectId,
    })),
    readProfile: vi.fn().mockResolvedValue({
      $typeName: 'user.v1.Profile',
      subjectId: new Uint8Array(16).fill(1),
      displayName: '',
      bio: '',
      version: 1n,
      createdAtUnix: 1n,
      updatedAtUnix: 1n,
    }),
    updateProfile: vi.fn(),
    readAddresses: vi.fn().mockResolvedValue(snapshot()),
    createAddress: vi.fn().mockResolvedValue(snapshot(2n, [address(), address(2, 'Борис')])),
    updateAddress: vi.fn().mockResolvedValue(snapshot(2n)),
    deleteAddress: vi.fn().mockResolvedValue(snapshot(2n, [])),
    setDefaultAddress: vi.fn().mockResolvedValue(snapshot(2n)),
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
  await router.push('/account/addresses');
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
async function fill(wrapper: VueWrapper) {
  for (const [key, value] of Object.entries(fields('Борис')))
    await wrapper.find(`#address-${key}`).setValue(value);
}
async function submit(wrapper: VueWrapper) {
  await wrapper.find('form').trigger('submit');
  await flushPromises();
}

describe('address book', () => {
  it('creates with book CAS, renders multiple literal records, confirms deletion and replaces snapshots', async () => {
    const { session } = fixture();
    const { wrapper } = await open(session);
    await button(wrapper, 'Добавить адрес').trigger('click');
    await fill(wrapper);
    await submit(wrapper);
    expect(session.createAddress).toHaveBeenCalledWith(
      { fields: fields('Борис'), expectedBookVersion: 1n },
      { generation: 'g1', subjectId: '01'.repeat(16) },
    );
    expect(wrapper.findAll('.address-card')).toHaveLength(2);
    await button(wrapper, 'Удалить адрес Анна').trigger('click');
    expect(session.deleteAddress).not.toHaveBeenCalled();
    await button(wrapper, 'Подтвердить удаление').trigger('click');
    await flushPromises();
    expect(session.deleteAddress).toHaveBeenCalledWith(
      { addressId: address().addressId, expectedBookVersion: 2n },
      expect.anything(),
    );
    expect(wrapper.findAll('.address-card')).toHaveLength(0);
  });
  it('updates fields and selects a default without changing other records', async () => {
    const { session } = fixture();
    vi.mocked(session.readAddresses).mockResolvedValue(
      snapshot(7n, [address(), address(2, 'Борис')]),
    );
    vi.mocked(session.setDefaultAddress).mockResolvedValue(
      snapshot(8n, [
        addressValue({ ...address(), isDefault: false }),
        addressValue({ ...address(2, 'Борис'), isDefault: true }),
      ]),
    );
    const { wrapper } = await open(session);
    await button(wrapper, 'Использовать по умолчанию').trigger('click');
    await flushPromises();
    expect(session.setDefaultAddress).toHaveBeenCalledWith(
      { addressId: address(2).addressId, expectedBookVersion: 7n },
      expect.anything(),
    );
    expect(wrapper.findAll('.address-card')[1]!.text()).toContain('По умолчанию');
    await button(wrapper, 'Изменить адрес Анна').trigger('click');
    await wrapper.find('#address-comment').setValue('<script>текст</script>');
    await submit(wrapper);
    expect(session.updateAddress).toHaveBeenCalledWith(
      {
        addressId: address().addressId,
        expectedBookVersion: 8n,
        fields: { ...fields(), comment: '<script>текст</script>' },
      },
      expect.anything(),
    );
    expect(wrapper.find('script').exists()).toBe(false);
  });
  it('keeps draft and CAS through a conflict until the user reconciles', async () => {
    const { session } = fixture();
    vi.mocked(session.updateAddress).mockRejectedValueOnce(
      new ConnectError('private', Code.Aborted),
    );
    const { wrapper } = await open(session);
    await button(wrapper, 'Изменить').trigger('click');
    await wrapper.find('#address-city').setValue('Тверь');
    await submit(wrapper);
    expect(wrapper.text()).not.toContain('private');
    await submit(wrapper);
    expect(session.updateAddress).toHaveBeenCalledTimes(1);
    vi.mocked(session.readAddresses).mockResolvedValueOnce(snapshot(9n));
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    expect((wrapper.find('#address-city').element as HTMLInputElement).value).toBe('Тверь');
    await button(wrapper, 'Оставить мой черновик').trigger('click');
    await submit(wrapper);
    expect(vi.mocked(session.updateAddress).mock.calls[1]![0].expectedBookVersion).toBe(9n);
  });
  it('never repeats unknown creation and requires explicit duplicate-aware choice after reread', async () => {
    const { session } = fixture();
    vi.mocked(session.createAddress).mockRejectedValueOnce(new TypeError('lost reply'));
    const { wrapper } = await open(session);
    await button(wrapper, 'Добавить').trigger('click');
    await fill(wrapper);
    await submit(wrapper);
    await submit(wrapper);
    expect(session.createAddress).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('мог уже сохраниться');
    vi.mocked(session.readAddresses).mockResolvedValueOnce(
      snapshot(2n, [address(), address(2, 'Борис')]),
    );
    await button(wrapper, 'Перечитать актуальные').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('.address-card')).toHaveLength(2);
    expect(session.createAddress).toHaveBeenCalledTimes(1);
    await button(wrapper, 'Принять актуальную').trigger('click');
    expect(wrapper.find('form').exists()).toBe(false);
    expect(session.createAddress).toHaveBeenCalledTimes(1);
  });
  it('cannot resurrect an externally deleted address, and clears private drafts on owner change', async () => {
    const { session, state } = fixture();
    const missing = new ConnectError('absent', Code.NotFound);
    missing.details.push(missingDetail());
    vi.mocked(session.updateAddress).mockRejectedValueOnce(missing);
    const { wrapper } = await open(session);
    await button(wrapper, 'Изменить').trigger('click');
    await wrapper.find('#address-city').setValue('Секретный город');
    await submit(wrapper);
    vi.mocked(session.readAddresses).mockResolvedValueOnce(snapshot(3n, []));
    await button(wrapper, 'Перечитать').trigger('click');
    await flushPromises();
    expect(wrapper.text()).toContain('Редактируемый адрес удалён');
    expect(wrapper.text()).not.toContain('Оставить мой черновик');
    state.value = { status: 'switching', generation: 'g2', subjectId: null };
    await flushPromises();
    expect(wrapper.find('form').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('Секретный город');
    expect(wrapper.text()).not.toContain('Анна');
  });
  it('preserves drafts during same-owner refresh and warns about leaving', async () => {
    const { session, state } = fixture();
    const { wrapper, router } = await open(session);
    await button(wrapper, 'Изменить').trigger('click');
    await wrapper.find('#address-comment').setValue('Черновик');
    const event = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    await router.push('/account');
    expect(router.currentRoute.value.path).toBe('/account/addresses');
    state.value = { ...state.value, status: 'checking' };
    await flushPromises();
    state.value = { ...state.value, status: 'authenticated' };
    await flushPromises();
    expect((wrapper.find('#address-comment').element as HTMLTextAreaElement).value).toBe(
      'Черновик',
    );
  });
  it('discards late responses and late failures after logout', async () => {
    const { session, state } = fixture();
    let resolve!: (book: AddressBook) => void;
    vi.mocked(session.readAddresses).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const { wrapper } = await open(session);
    state.value = { status: 'anonymous', generation: 'g2', subjectId: null };
    resolve(snapshot());
    await flushPromises();
    expect(wrapper.text()).not.toContain('Анна');
    expect(wrapper.findAll('.address-card')).toHaveLength(0);
  });
  it('bounds profile provisioning polls and treats plain NotFound as an error', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const { session } = fixture('profilePending');
    vi.mocked(session.readProfile).mockRejectedValue(new ConnectError('pending', Code.NotFound));
    const { wrapper, router } = await open(session);
    for (const delay of [1500, 3000, 6000, 12000]) {
      await vi.advanceTimersByTimeAsync(delay);
      await flushPromises();
    }
    expect(session.readProfile).toHaveBeenCalledTimes(4);
    expect(wrapper.text()).toContain('Автоматическая проверка приостановлена');
    await router.push('/login');
    await vi.advanceTimersByTimeAsync(60000);
    expect(session.readProfile).toHaveBeenCalledTimes(4);
    const other = fixture();
    vi.mocked(other.session.readAddresses).mockRejectedValue(
      new ConnectError('PROFILE_NOT_READY', Code.NotFound),
    );
    const view = await open(other.session);
    expect(view.wrapper.text()).toContain('Адреса пока недоступны');
  });
});
