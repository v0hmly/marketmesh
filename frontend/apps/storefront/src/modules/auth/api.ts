import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import type { SessionApi } from '../../shell/session/contracts';
import { createAuthTransport } from './transport';

/** Cookie mutation and identity probe have separate server-verified contracts. */
export function createAuthApi(
  getIdentity: SessionApi['getIdentity'],
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
): SessionApi {
  const auth = createClient(AuthService, createAuthTransport(origin, fetcher));
  return {
    getIdentity,
    async register(identifier, password) {
      await auth.registerCredentials({ identifier, password });
    },
    async confirmEmail(token) {
      const result = await auth.confirmEmail({ token });
      if (result.subjectId.length === 0) return null;
      if (result.subjectId.length !== 16) throw new Error('Invalid confirmation response');
      return result.subjectId;
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
  };
}
