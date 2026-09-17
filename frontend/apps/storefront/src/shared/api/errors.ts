import { Code, ConnectError } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';

export function isProfilePending(error: unknown): boolean {
  if (!(error instanceof ConnectError) || error.code !== Code.NotFound) return false;
  const details = error.findDetails(ErrorInfoSchema);
  return (
    details.length === 1 &&
    details[0]?.domain === 'marketmesh.user' &&
    details[0].reason === 'PROFILE_NOT_READY' &&
    Object.keys(details[0].metadata).length === 0
  );
}

export function isAddressError(
  error: unknown,
  reason: 'ADDRESS_NOT_FOUND' | 'ADDRESS_LIMIT_REACHED',
): boolean {
  if (
    !(error instanceof ConnectError) ||
    error.code !== (reason === 'ADDRESS_NOT_FOUND' ? Code.NotFound : Code.ResourceExhausted)
  )
    return false;
  const details = error.findDetails(ErrorInfoSchema);
  return (
    details.length === 1 &&
    details[0]?.domain === 'marketmesh.user' &&
    details[0].reason === reason &&
    Object.keys(details[0].metadata).length === 0
  );
}
