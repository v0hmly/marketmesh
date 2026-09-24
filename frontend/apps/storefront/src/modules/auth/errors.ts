import { Code, ConnectError } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';

/** Reads a single ErrorInfo reason of the auth.v1 domain, nothing else. */
export function authErrorReason(
  error: unknown,
  code: Code,
  reason:
    | 'LOGIN_LOCKED'
    | 'CODE_MISMATCH'
    | 'CODE_REISSUED'
    | 'CODE_EXPIRED'
    | 'TOKEN_EXPIRED'
    | 'TOKEN_USED'
    | 'EMAIL_UNVERIFIED'
    | 'RATE_LIMITED'
    | 'NEW_DEVICE_COOLDOWN',
): boolean {
  if (!(error instanceof ConnectError) || error.code !== code) return false;
  const details = error.findDetails(ErrorInfoSchema);
  return (
    details.length === 1 &&
    details[0]?.domain === 'marketmesh.auth' &&
    details[0].reason === reason &&
    Object.keys(details[0].metadata).length === 0
  );
}
