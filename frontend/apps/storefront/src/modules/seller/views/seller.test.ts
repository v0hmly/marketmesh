import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { shallowRef } from 'vue';
import { createMemoryHistory } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Shop, SellerApi } from '../../../shared/api/seller';
import type { SessionController, SessionState } from '../../../shell/session';
import { sellerApiKey, sessionKey } from '../../../shell/context';
import { createStorefrontRouter } from '../../../shell/router';

vi.mock('../../../shared/features', () => ({
  avatarEnabled: false,
  addressesEnabled: false,
  settingsEnabled: false,
  ordersEnabled: false,
  favoritesEnabled: false,
  reviewsEnabled: false,
  idEnabled: false,
  sellerEnabled: true,
  staffEnabled: false,
}));

// Hand-encoded google.rpc.ErrorInfo { reason: 1, domain: 2 } — tests must not
// import the generated API boundary directly.
function errorInfoValue(domain: string, reason: string): Uint8Array {
  const utf8 = new TextEncoder();
  const field = (number: number, text: string): number[] => {
    const bytes = utf8.encode(text);
    return [(number << 3) | 2, bytes.length, ...bytes];
  };
  return new Uint8Array([...field(1, reason), ...field(2, domain)]);
}
function domainError(code: Code, domain: string, reason: string) {
  const error = new ConnectError('untrusted message', code);
  error.details = [{ type: 'google.rpc.ErrorInfo', value: errorInfoValue(domain, reason) }];
  return error;
}
function sellerError(code: Code, reason: string) {
  return domainError(code, 'marketmesh.seller', reason);
}
function authError(code: Code, reason: string) {
  return domainError(code, 'marketmesh.auth', reason);
}

const shop = (overrides: Partial<Shop> = {}): Shop => ({
  shopId: new Uint8Array(16).fill(7),
  shopName: 'Мастерская «Глина и соль»',
  status: 'approved',
  rejectReason: '',
  fixInstructions: '',
  createdAtUnix: 1n,
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
  const sellerApi: SellerApi = {
    submitApplication: vi.fn().mockResolvedValue(undefined),
    getMyShop: vi.fn().mockResolvedValue(shop()),
  };
  return { session, state, sellerApi };
}
const mounted: VueWrapper[] = [];
beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {});
});
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
});
async function open(session: SessionController, sellerApi: SellerApi, path: string) {
  const router = createStorefrontRouter(createMemoryHistory());
  await router.push(path);
  await router.isReady();
  const wrapper = mount(
    { template: '<RouterView />' },
    {
      global: {
        plugins: [router],
        provide: { [sessionKey as symbol]: session, [sellerApiKey as symbol]: sellerApi },
      },
    },
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
async function fillCredentials(wrapper: VueWrapper) {
  await wrapper.find('#seller-email').setValue('anna@example.ru');
  await wrapper.find('#seller-password').setValue('long passphrase');
}

describe('seller login', () => {
  it('validates credentials on submit and never starts login for empty fields', async () => {
    const { session, sellerApi } = fixture('anonymous');
    const { wrapper } = await open(session, sellerApi, '/seller/login');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#seller-email-error').exists()).toBe(true);
    expect(wrapper.find('#seller-password-error').exists()).toBe(true);
    expect(session.startLogin).not.toHaveBeenCalled();
  });

  it('shows an unavailable state when the backend has no code step', async () => {
    const { session, sellerApi } = fixture('anonymous');
    vi.mocked(session.startLogin).mockRejectedValue(
      new ConnectError('no code step', Code.Unimplemented),
    );
    const { wrapper } = await open(session, sellerApi, '/seller/login');
    await fillCredentials(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Портал продавца пока недоступен.');
    expect(wrapper.find('#seller-email').exists()).toBe(false);
    expect(session.login).not.toHaveBeenCalled();
  });

  it('walks through the mandatory code step into the dashboard', async () => {
    const { session, sellerApi } = fixture('anonymous');
    const challenge = { challengeId: new Uint8Array(16).fill(9), codeExpiresInSeconds: 600n };
    vi.mocked(session.startLogin).mockResolvedValue(challenge);
    vi.mocked(session.completeLogin).mockRejectedValueOnce(
      authError(Code.InvalidArgument, 'CODE_MISMATCH'),
    );
    const { wrapper, router } = await open(session, sellerApi, '/seller/login');
    await fillCredentials(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('h1').text()).toBe('Подтвердите вход.');
    expect(wrapper.find('#seller-code-help').text()).toContain('10 минут');
    const codeInput = wrapper.find('#seller-code');
    await codeInput.setValue('48a29');
    expect((codeInput.element as HTMLInputElement).value).toBe('4829');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#seller-code-error').text()).toBe('Код состоит из шести цифр.');
    expect(session.completeLogin).not.toHaveBeenCalled();
    await codeInput.setValue('000000');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain('Код неверный.');
    vi.mocked(session.completeLogin).mockResolvedValue(undefined);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(sellerApi.getMyShop).toHaveBeenCalledTimes(1);
    expect(router.currentRoute.value.path).toBe('/seller');
  });

  it('treats an unimplemented GetMyShop as the transitional approved state', async () => {
    const { session, sellerApi } = fixture('anonymous');
    vi.mocked(session.startLogin).mockResolvedValue({
      challengeId: new Uint8Array(16).fill(9),
      codeExpiresInSeconds: 600n,
    });
    vi.mocked(session.completeLogin).mockResolvedValue(undefined);
    vi.mocked(sellerApi.getMyShop).mockRejectedValue(
      new ConnectError('not yet', Code.Unimplemented),
    );
    const { wrapper, router } = await open(session, sellerApi, '/seller/login');
    await fillCredentials(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    await wrapper.find('#seller-code').setValue('482913');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(router.currentRoute.value.path).toBe('/seller');
  });

  it.each([
    {
      status: 'pending' as const,
      title: 'Заявка на проверке.',
      text: 'Мы напишем на вашу рабочую почту',
    },
    {
      status: 'rejected' as const,
      title: 'Заявка отклонена.',
      text: 'Фотографии товара нечитаемы',
    },
    {
      status: 'blocked' as const,
      title: 'Доступ к порталу приостановлен.',
      text: 'поддержку продавцов',
    },
  ])('shows the $status shop state after the code step', async ({ status, title, text }) => {
    const { session, sellerApi } = fixture('anonymous');
    vi.mocked(session.startLogin).mockResolvedValue({
      challengeId: new Uint8Array(16).fill(9),
      codeExpiresInSeconds: 600n,
    });
    vi.mocked(session.completeLogin).mockResolvedValue(undefined);
    vi.mocked(sellerApi.getMyShop).mockResolvedValue(
      shop({
        status,
        rejectReason: status === 'rejected' ? 'Фотографии товара нечитаемы.' : '',
        fixInstructions: status === 'rejected' ? 'Переснимите изделия при дневном свете.' : '',
      }),
    );
    const { wrapper, router } = await open(session, sellerApi, '/seller/login');
    await fillCredentials(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    await wrapper.find('#seller-code').setValue('482913');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('h1').text()).toBe(title);
    expect(wrapper.text()).toContain(text);
    expect(router.currentRoute.value.path).toBe('/seller/login');
    if (status === 'rejected') {
      expect(wrapper.text()).toContain('Переснимите изделия при дневном свете.');
      expect(wrapper.find('a[href="/seller/apply"]').exists()).toBe(true);
    }
  });

  it('offers the application form when no shop is linked to the account', async () => {
    const { session, sellerApi } = fixture('anonymous');
    vi.mocked(session.startLogin).mockResolvedValue({
      challengeId: new Uint8Array(16).fill(9),
      codeExpiresInSeconds: 600n,
    });
    vi.mocked(session.completeLogin).mockResolvedValue(undefined);
    vi.mocked(sellerApi.getMyShop).mockRejectedValue(sellerError(Code.NotFound, 'SHOP_NOT_FOUND'));
    const { wrapper } = await open(session, sellerApi, '/seller/login');
    await fillCredentials(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    await wrapper.find('#seller-code').setValue('482913');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('h1').text()).toBe('Магазина пока нет.');
    expect(wrapper.find('a[href="/seller/apply"]').exists()).toBe(true);
  });
});

describe('seller application', () => {
  async function fillApplication(wrapper: VueWrapper, inn = '7707083893') {
    await wrapper.find('#apply-email').setValue('anna@example.ru');
    await wrapper.find('#apply-password').setValue('Secret123!');
    await wrapper.find('#apply-confirm').setValue('Secret123!');
    await wrapper.find('#apply-shop').setValue('Мастерская «Глина и соль»');
    await wrapper.find('#apply-inn').setValue(inn);
  }

  it('validates the INN and keeps the shop name counter', async () => {
    const { session, sellerApi } = fixture('anonymous');
    const { wrapper } = await open(session, sellerApi, '/seller/apply');
    expect(wrapper.find('#apply-shop-count').text()).toBe('0 / 80');
    await fillApplication(wrapper, '770708389');
    expect(wrapper.find('#apply-shop-count').text()).toContain('/ 80');
    await wrapper.find('form').trigger('submit');
    expect(wrapper.find('#apply-inn-error').text()).toBe('ИНН состоит из 10 или 12 цифр.');
    expect(sellerApi.submitApplication).not.toHaveBeenCalled();
    await wrapper.find('#apply-inn').setValue('123 456 789 012');
    expect((wrapper.find('#apply-inn').element as HTMLInputElement).value).toBe('123456789012');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(sellerApi.submitApplication).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('Заявка отправлена.');
    expect(wrapper.find('a[href="/seller/login"]').exists()).toBe(true);
  });

  it('maps seller conflicts onto the matching fields', async () => {
    const { session, sellerApi } = fixture('anonymous');
    vi.mocked(sellerApi.submitApplication).mockRejectedValue(
      sellerError(Code.FailedPrecondition, 'EMAIL_TAKEN'),
    );
    const { wrapper } = await open(session, sellerApi, '/seller/apply');
    await fillApplication(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('#apply-email-error').text()).toContain('Эта почта уже зарегистрирована.');
    expect(wrapper.find('#apply-email').attributes('aria-invalid')).toBe('true');
    vi.mocked(sellerApi.submitApplication).mockRejectedValue(
      sellerError(Code.FailedPrecondition, 'NAME_TAKEN'),
    );
    await wrapper.find('#apply-email').setValue('anna@example.ru');
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('#apply-shop-error').text()).toContain('Такое название уже занято');
  });

  it('shows an unavailable state while the backend cannot accept applications', async () => {
    const { session, sellerApi } = fixture('anonymous');
    vi.mocked(sellerApi.submitApplication).mockRejectedValue(
      new ConnectError('not yet', Code.Unimplemented),
    );
    const { wrapper } = await open(session, sellerApi, '/seller/apply');
    await fillApplication(wrapper);
    await wrapper.find('form').trigger('submit');
    await flushPromises();
    expect(wrapper.text()).toContain('Приём заявок пока недоступен.');
  });

  it('shows the password checklist only after typing starts', async () => {
    const { session, sellerApi } = fixture('anonymous');
    const { wrapper } = await open(session, sellerApi, '/seller/apply');
    expect(wrapper.find('#apply-password-requirements').exists()).toBe(false);
    await wrapper.find('#apply-password').setValue('Secret1');
    expect(wrapper.findAll('.password-requirements li')).toHaveLength(5);
  });
});

describe('seller sections', () => {
  it.each(['/seller', '/seller/products', '/seller/orders'])(
    'asks anonymous visitors to sign in on %s',
    async (path) => {
      const { session, sellerApi } = fixture('anonymous');
      const { wrapper } = await open(session, sellerApi, path);
      expect(wrapper.text()).toContain('Портал продавца начинается со входа');
      expect(wrapper.find('a[href="/seller/login"]').exists()).toBe(true);
    },
  );

  it('lists tasks on the dashboard and settles them with feedback', async () => {
    const { session, sellerApi } = fixture();
    const { wrapper } = await open(session, sellerApi, '/seller');
    expect(wrapper.text()).toContain('Магазин «Мастерская «Глина и соль»».');
    expect(wrapper.findAll('.seller-task')).toHaveLength(3);
    await button(wrapper, 'Подтвердить').trigger('click');
    expect(wrapper.findAll('.seller-task')).toHaveLength(2);
    expect(wrapper.find('[role="status"]').text()).toContain('Заказ подтверждён.');
    await button(wrapper, 'Передать').trigger('click');
    await button(wrapper, 'Открыть').trigger('click');
    expect(wrapper.text()).toContain('Ничего не ждёт');
  });

  it('filters products and toggles publication with feedback', async () => {
    const { session, sellerApi } = fixture();
    const { wrapper } = await open(session, sellerApi, '/seller/products');
    expect(wrapper.findAll('.sample-card')).toHaveLength(4);
    await button(wrapper, 'На модерации').trigger('click');
    expect(wrapper.findAll('.sample-card')).toHaveLength(1);
    await button(wrapper, 'Все').trigger('click');
    await button(wrapper, 'Снять с продажи').trigger('click');
    expect(wrapper.text()).toContain('Изделие скрыто. Резерв по оформленным заказам сохраняется.');
    await button(wrapper, 'Скрыты').trigger('click');
    expect(wrapper.findAll('.sample-card')).toHaveLength(1);
  });

  it('advances orders and filters done ones', async () => {
    const { session, sellerApi } = fixture();
    const { wrapper } = await open(session, sellerApi, '/seller/orders');
    expect(wrapper.findAll('.sample-card')).toHaveLength(2);
    await button(wrapper, 'Подтвердить заказ').trigger('click');
    expect(wrapper.find('[role="status"]').text()).toContain('Заказ подтверждён.');
    await button(wrapper, 'Завершённые').trigger('click');
    expect(wrapper.findAll('.sample-card')).toHaveLength(1);
    expect(wrapper.text()).toContain('Заказ № 1471-0088');
    await button(wrapper, 'Отменённые').trigger('click');
    expect(wrapper.text()).toContain('Отменённых заказов нет.');
  });
});
