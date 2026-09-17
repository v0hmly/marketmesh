import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import {
  GetMeResponseSchema,
  ProfileSchema,
  UpdateMeRequestSchema,
} from '@marketmesh/api/user/v1/user_pb';
import { describe, expect, it, vi } from 'vitest';
import { createPublicApi } from './client';

const profile = create(ProfileSchema, {
  subjectId: Uint8Array.from({ length: 16 }, () => 7),
  displayName: 'Мастер 🪡',
  bio: 'Обычный текст',
  version: 9007199254740993n,
});
const response = () =>
  new Response(toBinary(GetMeResponseSchema, create(GetMeResponseSchema, { profile })), {
    headers: { 'content-type': 'application/proto' },
  });

describe('public browser API boundary', () => {
  it('uses one-origin POST, browser cookies and no-store, with exact bigint versions', async () => {
    const requests: Request[] = [];
    const fetcher = vi.fn<typeof fetch>(async (input, init) => {
      requests.push(new Request(input, init));
      return response();
    });
    const api = createPublicApi('https://marketmesh.test', fetcher);
    expect((await api.getProfile()).version).toBe(profile.version);
    await api.updateProfile({
      displayName: 'Имя',
      bio: '<script>text</script>',
      expectedVersion: profile.version,
    });
    expect(requests.map((r) => new URL(r.url).pathname)).toEqual([
      '/user.v1.UserService/GetMe',
      '/user.v1.UserService/UpdateMe',
    ]);
    for (const request of requests) {
      expect(request.method).toBe('POST');
      expect(request.credentials).toBe('same-origin');
      expect(request.cache).toBe('no-store');
      expect(request.redirect).toBe('error');
      expect(request.referrerPolicy).toBe('no-referrer');
      expect(request.headers.has('authorization')).toBe(false);
      expect(request.headers.has('cookie')).toBe(false);
    }
    const input = fromBinary(
      UpdateMeRequestSchema,
      new Uint8Array(await requests[1]!.arrayBuffer()),
    );
    expect(input.expectedVersion).toBe(profile.version);
    expect(input.bio).toBe('<script>text</script>');
  });

  it('never replays a mutation after an unknown network result', async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new TypeError('Network failure'));
    const api = createPublicApi('https://marketmesh.test', fetcher);
    await expect(
      api.updateProfile({ displayName: '', bio: '', expectedVersion: 1n }),
    ).rejects.toBeInstanceOf(ConnectError);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(Object.keys(api).sort()).toEqual([
      'getProfile',
      'login',
      'logout',
      'logoutAll',
      'refresh',
      'register',
      'updateProfile',
    ]);
  });

  it('rejects malformed profile identity instead of authorizing an unknown owner', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(new Uint8Array(), {
        headers: { 'content-type': 'application/proto' },
      }),
    );
    await expect(
      createPublicApi('https://marketmesh.test', fetcher).getProfile(),
    ).rejects.toMatchObject({ code: Code.DataLoss });
  });

  it.each([
    'http://localhost:5173',
    'https://user:password@marketmesh.test',
    'https://marketmesh.test/api',
  ])('rejects unsafe API origin %s', (origin) => {
    expect(() => createPublicApi(origin)).toThrow();
  });
});
