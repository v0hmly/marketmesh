import { create, toBinary } from '@bufbuild/protobuf';
import { ConnectError } from '@connectrpc/connect';
import { Code } from '@connectrpc/connect';
import { ErrorInfoSchema } from '@marketmesh/api/google/rpc/error_details_pb';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import { createClient } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { createPublicTransport } from '@marketmesh/browser-client/transport';
import { authErrorReason } from '../../modules/auth/errors';

describe('wire ErrorInfo', () => {
  it('parses CODE_MISMATCH details from a JSON connect error', async () => {
    const fetcher = (async () => {
      const detail = toBinary(
        ErrorInfoSchema,
        create(ErrorInfoSchema, { domain: 'marketmesh.auth', reason: 'CODE_MISMATCH' }),
      );
      return new Response(
        JSON.stringify({
          code: 'invalid_argument',
          message: 'wrong code',
          details: [
            { type: 'google.rpc.ErrorInfo', value: Buffer.from(detail).toString('base64') },
          ],
        }),
        { status: 400, headers: { 'content-type': 'application/json' } },
      );
    }) as typeof fetch;
    const client = createClient(
      AuthService,
      createPublicTransport('https://marketmesh.test', fetcher),
    );
    let caught: unknown;
    try {
      await client.completeLogin({ loginChallengeId: new Uint8Array(16).fill(9), code: '000000' });
    } catch (error) {
      caught = error;
    }
    expect(caught).toBeInstanceOf(ConnectError);
    expect((caught as ConnectError).code).toBe(Code.InvalidArgument);
    expect(authErrorReason(caught, Code.InvalidArgument, 'CODE_MISMATCH')).toBe(true);
  });
});
