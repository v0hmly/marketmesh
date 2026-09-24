import { create, toBinary } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import { describe, expect, it } from 'vitest';
import { isAddressError, isProfilePending } from '../../modules/account/api/errors';
import { sellerErrorReason } from '../../modules/seller/errors';

function pending(domain = 'marketmesh.user', reason = 'PROFILE_NOT_READY', metadata = {}) {
  const error = new ConnectError('untrusted message', Code.NotFound);
  error.details = [
    {
      type: 'google.rpc.ErrorInfo',
      value: toBinary(ErrorInfoSchema, create(ErrorInfoSchema, { domain, reason, metadata })),
    },
  ];
  return error;
}

describe('profile provisioning signal', () => {
  it('accepts the exact protobuf ErrorInfo, independently of the human message', () => {
    expect(isProfilePending(pending())).toBe(true);
  });
  it('rejects generic not-found, wrong domain/reason, extra metadata and malformed details', () => {
    expect(isProfilePending(new ConnectError('PROFILE_NOT_READY', Code.NotFound))).toBe(false);
    expect(isProfilePending(pending('other.service'))).toBe(false);
    expect(isProfilePending(pending('marketmesh.user', 'OTHER'))).toBe(false);
    expect(
      isProfilePending(pending('marketmesh.user', 'PROFILE_NOT_READY', { owner: 'untrusted' })),
    ).toBe(false);
    const error = pending();
    error.details = [
      {
        type: 'google.rpc.ErrorInfo',
        value: new Uint8Array([255]),
        debug: { domain: 'marketmesh.user', reason: 'PROFILE_NOT_READY' },
      },
    ];
    expect(isProfilePending(error)).toBe(false);
  });
  it('rejects an ambiguous duplicate or the same detail with the wrong status', () => {
    const error = pending();
    error.details.push(...error.details);
    expect(isProfilePending(error)).toBe(false);
    const denied = new ConnectError('no', Code.PermissionDenied);
    denied.details = pending().details;
    expect(isProfilePending(denied)).toBe(false);
  });
});

describe('address error signals', () => {
  it('requires exact reason, domain, status and unique metadata-free details', () => {
    const absent = pending('marketmesh.user', 'ADDRESS_NOT_FOUND');
    expect(isAddressError(absent, 'ADDRESS_NOT_FOUND')).toBe(true);
    expect(isProfilePending(absent)).toBe(false);
    expect(
      isAddressError(new ConnectError('ADDRESS_NOT_FOUND', Code.NotFound), 'ADDRESS_NOT_FOUND'),
    ).toBe(false);
    expect(isAddressError(pending('other', 'ADDRESS_NOT_FOUND'), 'ADDRESS_NOT_FOUND')).toBe(false);
    expect(
      isAddressError(
        pending('marketmesh.user', 'ADDRESS_NOT_FOUND', { id: 'x' }),
        'ADDRESS_NOT_FOUND',
      ),
    ).toBe(false);
    const limit = new ConnectError('limit', Code.ResourceExhausted);
    limit.details = pending('marketmesh.user', 'ADDRESS_LIMIT_REACHED').details;
    expect(isAddressError(limit, 'ADDRESS_LIMIT_REACHED')).toBe(true);
    expect(
      isAddressError(pending('marketmesh.user', 'ADDRESS_LIMIT_REACHED'), 'ADDRESS_LIMIT_REACHED'),
    ).toBe(false);
    absent.details.push(...absent.details);
    expect(isAddressError(absent, 'ADDRESS_NOT_FOUND')).toBe(false);
  });
});

describe('seller error signals', () => {
  const conflict = (reason: string) => {
    const error = new ConnectError('untrusted message', Code.FailedPrecondition);
    error.details = pending('marketmesh.seller', reason).details;
    return error;
  };
  it('accepts the exact seller reason with the declared status', () => {
    expect(sellerErrorReason(conflict('EMAIL_TAKEN'), Code.FailedPrecondition, 'EMAIL_TAKEN')).toBe(
      true,
    );
    expect(sellerErrorReason(conflict('NAME_TAKEN'), Code.FailedPrecondition, 'NAME_TAKEN')).toBe(
      true,
    );
  });
  it('rejects wrong status, domain, reason and ambiguous details', () => {
    expect(sellerErrorReason(conflict('EMAIL_TAKEN'), Code.NotFound, 'EMAIL_TAKEN')).toBe(false);
    expect(sellerErrorReason(conflict('EMAIL_TAKEN'), Code.FailedPrecondition, 'INN_TAKEN')).toBe(
      false,
    );
    expect(
      sellerErrorReason(
        new ConnectError('no', Code.FailedPrecondition),
        Code.FailedPrecondition,
        'EMAIL_TAKEN',
      ),
    ).toBe(false);
    const doubled = conflict('EMAIL_TAKEN');
    doubled.details.push(...doubled.details);
    expect(sellerErrorReason(doubled, Code.FailedPrecondition, 'EMAIL_TAKEN')).toBe(false);
    const buyer = new ConnectError('no', Code.FailedPrecondition);
    buyer.details = pending('marketmesh.user', 'EMAIL_TAKEN').details;
    expect(sellerErrorReason(buyer, Code.FailedPrecondition, 'EMAIL_TAKEN')).toBe(false);
  });
  it('reads SHOP_NOT_FOUND from the NotFound status', () => {
    const missing = new ConnectError('no', Code.NotFound);
    missing.details = pending('marketmesh.seller', 'SHOP_NOT_FOUND').details;
    expect(sellerErrorReason(missing, Code.NotFound, 'SHOP_NOT_FOUND')).toBe(true);
  });
});
