import { inject, type InjectionKey } from 'vue';
import type { SessionController, SessionGuard } from '../../../shell/session';
import type {
  AccountApi,
  ProfileInput,
  SettingsInput,
  AddressWrite,
  AddressSelection,
} from './types';

/** Domain operations keep their ownership checks and never replay a write. */
export function createAccountController(session: SessionController, api: AccountApi) {
  return {
    readProfile: (guard?: SessionGuard) => session.readOwned(() => api.getProfile(), guard),
    updateProfile: (input: ProfileInput, guard: SessionGuard) =>
      session.writeOwned(() => api.updateProfile(input), guard),
    readSettings: (guard: SessionGuard) => session.readOwned(() => api.getSettings(), guard),
    updateSettings: (input: SettingsInput, guard: SessionGuard) =>
      session.writeOwned(() => api.updateSettings(input), guard),
    readAddresses: (guard: SessionGuard) => session.readOwned(() => api.listAddresses(), guard),
    createAddress: (input: AddressWrite, guard: SessionGuard) =>
      session.writeOwned(() => api.createAddress(input), guard),
    updateAddress: (input: AddressWrite & AddressSelection, guard: SessionGuard) =>
      session.writeOwned(() => api.updateAddress(input), guard),
    deleteAddress: (input: AddressSelection, guard: SessionGuard) =>
      session.writeOwned(() => api.deleteAddress(input), guard),
    setDefaultAddress: (input: AddressSelection, guard: SessionGuard) =>
      session.writeOwned(() => api.setDefaultAddress(input), guard),
  };
}
export type AccountController = ReturnType<typeof createAccountController>;
export const accountKey: InjectionKey<AccountController> = Symbol('marketmesh-account');
export function useAccount(): AccountController {
  const account = inject(accountKey);
  if (!account) throw new Error('Account controller is missing');
  return account;
}
