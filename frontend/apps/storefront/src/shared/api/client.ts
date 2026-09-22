import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import {
  Gender,
  Theme,
  type AccountSettings as WireSettings,
  UserService,
} from '@marketmesh/api/user/v1/user_pb';
import type { Profile, PublicApi, AddressBook, AccountSettings, ThemePreference } from './types';
import { createPublicTransport } from './transport';

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

/** The app and public gateway must share one HTTPS origin. No token APIs exist here. */
export function createPublicApi(
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
): PublicApi {
  const transport = createPublicTransport(origin, fetcher);
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
    async startLogin(identifier, password) {
      const result = await auth.startLogin({ identifier, password });
      if (result.subjectId.length !== 0) {
        if (
          result.subjectId.length !== 16 ||
          result.subjectId.every((v) => v === 0) ||
          result.loginChallengeId.length !== 0 ||
          result.codeExpiresInSeconds !== 0n
        )
          throw new ConnectError('Invalid start login response', Code.DataLoss);
        return { subjectId: result.subjectId };
      }
      if (
        result.loginChallengeId.length !== 16 ||
        result.loginChallengeId.every((v) => v === 0) ||
        result.codeExpiresInSeconds < 1n
      ) {
        throw new ConnectError('Invalid start login response', Code.DataLoss);
      }
      return {
        challengeId: result.loginChallengeId,
        codeExpiresInSeconds: result.codeExpiresInSeconds,
      };
    },
    async completeLogin(challengeId, code) {
      const result = await auth.completeLogin({ loginChallengeId: challengeId, code });
      if (result.subjectId.length !== 16 || result.subjectId.every((v) => v === 0)) {
        throw new ConnectError('Invalid complete login response', Code.DataLoss);
      }
      return result.subjectId;
    },
    async resendLoginCode(challengeId) {
      const result = await auth.resendLoginCode({ loginChallengeId: challengeId });
      if (result.codeExpiresInSeconds < 1n) {
        throw new ConnectError('Invalid resend code response', Code.DataLoss);
      }
      return { challengeId, codeExpiresInSeconds: result.codeExpiresInSeconds };
    },
    async requestEmailVerification(email) {
      await auth.requestEmailVerification({ email });
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
