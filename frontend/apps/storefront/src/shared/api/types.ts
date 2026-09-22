import {
  Gender,
  type Profile,
  type Address,
  type AddressBook,
  type AddressFields,
} from '@marketmesh/api/user/v1/user_pb';

export type { Profile, Address, AddressBook, AddressFields };
export { Gender };
export type AddressInput = Omit<AddressFields, '$typeName'>;
export interface AddressWrite {
  fields: AddressInput;
  expectedBookVersion: bigint;
}
export interface AddressSelection {
  addressId: Uint8Array;
  expectedBookVersion: bigint;
}

export type ThemePreference = 'system' | 'light' | 'dark';
export interface AccountSettings {
  subjectId: Uint8Array;
  version: bigint;
  theme: ThemePreference;
}
export interface SettingsInput {
  theme: ThemePreference;
  expectedVersion: bigint;
}

export interface ProfileInput {
  displayName: string;
  bio: string;
  expectedVersion: bigint;
  lastName?: string;
  birthDate?: string;
  gender?: Gender;
  phone?: string;
  city?: string;
  showAge?: boolean;
}

/** Pending login started by StartLogin; the code arrives by email. */
export interface LoginChallenge {
  challengeId: Uint8Array;
  codeExpiresInSeconds: bigint;
}

export type LoginStart = LoginChallenge | { subjectId: Uint8Array };

/** Only browser-public RPCs; cookies are managed exclusively by the browser. */
export interface PublicApi {
  register(identifier: string, password: Uint8Array): Promise<void>;
  login(identifier: string, password: Uint8Array): Promise<Uint8Array>;
  startLogin(identifier: string, password: Uint8Array): Promise<LoginStart>;
  completeLogin(challengeId: Uint8Array, code: string): Promise<Uint8Array>;
  resendLoginCode(challengeId: Uint8Array): Promise<LoginChallenge>;
  requestEmailVerification(email: string): Promise<void>;
  refresh(): Promise<void>;
  logout(): Promise<void>;
  logoutAll(): Promise<void>;
  getProfile(): Promise<Profile>;
  updateProfile(input: ProfileInput): Promise<Profile>;
  getSettings(): Promise<AccountSettings>;
  updateSettings(input: SettingsInput): Promise<AccountSettings>;
  listAddresses(): Promise<AddressBook>;
  createAddress(input: AddressWrite): Promise<AddressBook>;
  updateAddress(input: AddressWrite & AddressSelection): Promise<AddressBook>;
  deleteAddress(input: AddressSelection): Promise<AddressBook>;
  setDefaultAddress(input: AddressSelection): Promise<AddressBook>;
}
