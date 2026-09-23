import { createClient } from '@connectrpc/connect';
import { AuthService } from '@marketmesh/api/auth/v1/auth_pb';
import { UserService } from '@marketmesh/api/user/v1/user_pb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createPublicTransport } from './transport';

afterEach(() => vi.restoreAllMocks());

function fixture() {
  const requests: Request[] = [];
  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    requests.push(new Request(input, init));
    return new Response(new Uint8Array(), { headers: { 'content-type': 'application/proto' } });
  });
  const transport = createPublicTransport('https://marketmesh.test', fetcher);
  return {
    requests,
    auth: createClient(AuthService, transport),
    user: createClient(UserService, transport),
  };
}

describe('mail timezone metadata', () => {
  it('takes the browser IANA zone on each Auth request without sending it to User', async () => {
    const options = vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions');
    options.mockReturnValue({ timeZone: 'Europe/Moscow' } as Intl.ResolvedDateTimeFormatOptions);
    const { auth, user, requests } = fixture();
    await auth.requestEmailVerification({ email: 'buyer@example.test' });
    options.mockReturnValue({ timeZone: 'Asia/Kathmandu' } as Intl.ResolvedDateTimeFormatOptions);
    await auth.requestPasswordReset({ email: 'buyer@example.test' });
    await user.getMe({});
    expect(requests.map((r) => r.headers.get('X-MarketMesh-Time-Zone'))).toEqual([
      'Europe/Moscow',
      'Asia/Kathmandu',
      null,
    ]);
    expect(requests.every((r) => r.credentials === 'same-origin' && r.cache === 'no-store')).toBe(
      true,
    );
  });

  it('leaves Auth usable if the display preference is unavailable', async () => {
    vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockImplementation(() => {
      throw new Error('unavailable');
    });
    const { auth, requests } = fixture();
    await auth.requestEmailVerification({ email: 'buyer@example.test' });
    expect(requests[0]!.headers.has('X-MarketMesh-Time-Zone')).toBe(false);
  });
});
