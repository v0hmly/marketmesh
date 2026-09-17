import type { Profile } from '@marketmesh/api/user/v1/user_pb';

export type { Profile };

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
}
