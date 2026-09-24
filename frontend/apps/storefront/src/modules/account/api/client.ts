import { Code, ConnectError, createClient } from '@connectrpc/connect';
import {
  Gender,
  Theme,
  type AccountSettings as WireSettings,
  UserService,
} from '@marketmesh/api/user/v1/user_pb';
import type { Profile, AccountApi, AddressBook, AccountSettings, ThemePreference } from './types';
import { createPublicTransport } from '@marketmesh/browser-client/transport';

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

const wireThemes = { system: Theme.SYSTEM, light: Theme.LIGHT, dark: Theme.DARK } as const;
function validSettings(settings: WireSettings | undefined): AccountSettings {
  if (
    !settings ||
    settings.subjectId.length !== 16 ||
    settings.subjectId.every((value) => value === 0) ||
    settings.version < 1n ||
    settings.version > 9223372036854775807n ||
    ![Theme.SYSTEM, Theme.LIGHT, Theme.DARK].includes(settings.theme)
  )
    throw new ConnectError('Invalid settings response', Code.DataLoss);
  const theme: ThemePreference =
    settings.theme === Theme.DARK ? 'dark' : settings.theme === Theme.LIGHT ? 'light' : 'system';
  return { subjectId: settings.subjectId, version: settings.version, theme };
}

export function createAccountApi(
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
): AccountApi {
  const user = createClient(UserService, createPublicTransport(origin, fetcher));
  return {
    async getSettings() {
      return validSettings((await user.getSettings({})).settings);
    },
    async updateSettings(input) {
      if (!Object.hasOwn(wireThemes, input.theme))
        throw new ConnectError('Invalid theme', Code.InvalidArgument);
      return validSettings(
        (
          await user.updateSettings({
            theme: wireThemes[input.theme],
            expectedVersion: input.expectedVersion,
          })
        ).settings,
      );
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
      return validProfile(
        (
          await user.updateMe({
            displayName: input.displayName,
            bio: input.bio,
            expectedVersion: input.expectedVersion,
            lastName: input.lastName ?? '',
            birthDate: input.birthDate ?? '',
            gender: input.gender ?? Gender.UNSPECIFIED,
            phone: input.phone ?? '',
            city: input.city ?? '',
            showAge: input.showAge ?? false,
          })
        ).profile,
      );
    },
  };
}
