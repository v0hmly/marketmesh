import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import { UserService } from '@marketmesh/api/user/v1/user_pb';
import type { Profile, PublicApi, AddressBook } from './types';

function validProfile(profile: Profile | undefined): Profile {
  if (
    !profile ||
    profile.subjectId.length !== 16 ||
    profile.subjectId.every((v) => v === 0) ||
    profile.version < 1n ||
    profile.version > 9223372036854775807n
  ) {
    throw new ConnectError('Invalid profile response', Code.DataLoss);
  }
  return profile;
}

function validBook(book: AddressBook | undefined): AddressBook {
  const validId = (id: Uint8Array) => id.length === 16 && id.some((value) => value !== 0);
  const ids = new Set<string>();
  if (
    !book ||
    !validId(book.subjectId) ||
    book.version < 1n ||
    book.version > 9223372036854775807n ||
    book.addresses.length > 20
  )
    throw new ConnectError('Invalid address book response', Code.DataLoss);
  let defaults = 0;
  for (const address of book.addresses) {
    const key = Array.from(address.addressId).join(',');
    if (!validId(address.addressId) || !address.fields || ids.has(key))
      throw new ConnectError('Invalid address response', Code.DataLoss);
    ids.add(key);
    if (address.isDefault) defaults++;
  }
  if (defaults > 1) throw new ConnectError('Invalid default address response', Code.DataLoss);
  return book;
}

/** The app and public gateway must share one HTTPS origin. No token APIs exist here. */
export function createPublicApi(
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
): PublicApi {
  const url = new URL(origin);
  if (url.protocol !== 'https:' || url.username || url.password || url.origin !== origin) {
    throw new Error('Для входа требуется HTTPS origin приложения');
  }
  const transport = createConnectTransport({
    baseUrl: origin,
    useBinaryFormat: true,
    useHttpGet: false,
    defaultTimeoutMs: 15_000,
    fetch: (input, init) =>
      fetcher(input, {
        ...init,
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
      }),
  });
  const auth = createClient(AuthService, transport);
  const user = createClient(UserService, transport);
  return {
    async register(identifier, password) {
      await auth.registerCredentials({ identifier, password });
    },
    async login(identifier, password) {
      const result = await auth.login({ identifier, password });
      if (result.subjectId.length !== 16 || result.subjectId.every((v) => v === 0)) {
        throw new ConnectError('Invalid login response', Code.DataLoss);
      }
      return result.subjectId;
    },
    async refresh() {
      await auth.refreshSession({});
    },
    async logout() {
      await auth.logout({});
    },
    async logoutAll() {
      await auth.logoutAll({});
    },
    async listAddresses() {
      return validBook((await user.listAddresses({})).book);
    },
    async createAddress(input) {
      return validBook((await user.createAddress(input)).book);
    },
    async updateAddress(input) {
      return validBook((await user.updateAddress(input)).book);
    },
    async deleteAddress(input) {
      return validBook((await user.deleteAddress(input)).book);
    },
    async setDefaultAddress(input) {
      return validBook((await user.setDefaultAddress(input)).book);
    },
    async getProfile() {
      return validProfile((await user.getMe({})).profile);
    },
    async updateProfile(input) {
      return validProfile((await user.updateMe(input)).profile);
    },
  };
}
