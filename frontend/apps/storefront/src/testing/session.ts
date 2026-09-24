import {
  createSessionController as createCoreSession,
  type SessionController as CoreSession,
  type SessionEnvironment,
} from '../shell/session';
import { createAccountController, type AccountController } from '../modules/account/api/controller';
import { accountIdentityProbe } from '../shell/identity';
import { createAuthApi } from '../modules/auth/api';
import { createAccountApi } from '../modules/account/api/client';
import type { SessionApi } from '../shell/session/contracts';
import type { AccountApi } from '../modules/account/api/types';
export * from '../shell/session';
export * from '../modules/account/api/types';
export type SessionController = CoreSession & AccountController;
export type PublicApi = Omit<SessionApi, 'getIdentity'> & AccountApi;
export function createPublicApi(origin?: string, fetcher?: typeof fetch): PublicApi {
  const account = createAccountApi(origin, fetcher);
  return { ...account, ...createAuthApi(accountIdentityProbe(account), origin, fetcher) };
}
export function createSessionController(
  api: PublicApi,
  options?: { environment?: SessionEnvironment | null },
): SessionController {
  const session = createCoreSession({ ...api, getIdentity: accountIdentityProbe(api) }, options);
  return Object.assign(session, createAccountController(session, api));
}
