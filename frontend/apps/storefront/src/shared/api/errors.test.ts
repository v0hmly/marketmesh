import { create, toBinary } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import { describe, expect, it } from 'vitest';
import { isProfilePending } from './errors';

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
