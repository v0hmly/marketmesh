import { Code, ConnectError } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';

/** Reads a single ErrorInfo reason of the seller.v1 domain, nothing else. */
export function sellerErrorReason(
  error: unknown,
  code: Code,
  reason: 'EMAIL_TAKEN' | 'APPLICATION_PENDING' | 'INN_TAKEN' | 'NAME_TAKEN' | 'SHOP_NOT_FOUND',
): boolean {
  if (!(error instanceof ConnectError) || error.code !== code) return false;
  const details = error.findDetails(ErrorInfoSchema);
  return (
    details.length === 1 &&
    details[0]?.domain === 'marketmesh.seller' &&
    details[0].reason === reason &&
    Object.keys(details[0].metadata).length === 0
  );
}
