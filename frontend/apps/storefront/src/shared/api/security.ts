import { createClient } from '@connectrpc/connect';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import { createPublicTransport } from './transport';

type SecurityApi = ReturnType<typeof createSecurityApi>;
export type GetCredentialsResponse = Awaited<ReturnType<SecurityApi['getCredentials']>>;
export type SessionInfo = Awaited<ReturnType<SecurityApi['listSessions']>>['sessions'][number];

/** Public cookie-only security RPCs. SessionController coordinates each caller's owner and writes. */
export function createSecurityApi() {
  return createClient(AuthService, createPublicTransport(window.location.origin));
}
