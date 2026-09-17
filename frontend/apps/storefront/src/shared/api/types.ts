import type { Profile, Address, AddressBook, AddressFields } from '@marketmesh/api/user/v1/user_pb';

export type { Profile, Address, AddressBook, AddressFields };
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
}

/** Only browser-public RPCs; cookies are managed exclusively by the browser. */
export interface PublicApi {
  register(identifier: string, password: Uint8Array): Promise<void>;
  login(identifier: string, password: Uint8Array): Promise<Uint8Array>;
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
