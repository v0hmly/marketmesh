import { create, toBinary } from '@bufbuild/protobuf';
import { AvatarSchema, GetAvatarResponseSchema } from '@marketmesh/api/user/v1/user_pb';
import {
  GetStatusResponseSchema,
  CreateDownloadResponseSchema,
  UploadPartCapabilitySchema,
  FileState,
} from '@marketmesh/api/files/v1/files_pb';
import { describe, it, expect, vi } from 'vitest';
import { webcrypto } from 'node:crypto';
import { createAvatarApi, storageURL, uploadHeaders, validateAvatar } from './avatar';

describe('avatar boundary', () => {
  it('rejects foreign owners and malformed versions', () => {
    const a = create(AvatarSchema, { subjectId: new Uint8Array(16).fill(1), version: 1n });
    expect(() => validateAvatar(a, '02'.repeat(16))).toThrow();
    expect(validateAvatar(a, '01'.repeat(16))).toBe(a);
    expect(() => validateAvatar({ ...a, fileId: new Uint8Array(16) }, '01'.repeat(16))).toThrow();
  });
  it('constrains signed URLs and headers before sending any bytes', () => {
    const now = 1_000_000;
    const until = 1030n;
    for (const url of [
      'http://storage.test/key?sig=a',
      'https://other.test/key?sig=a',
      'https://user@storage.test/key?sig=a',
      'https://storage.test/key?sig=a#fragment',
    ])
      expect(() => storageURL(url, ['https://storage.test'], until, now)).toThrow();
    expect(() =>
      storageURL('https://storage.test/key?sig=a', ['https://storage.test'], 999n, now),
    ).toThrow();
    expect(() =>
      storageURL('https://storage.test/key?sig=a', ['https://storage.test'], 1100n, now),
    ).toThrow();
    const file = new File(['a'], 'image.png', { type: 'image/png' });
    const part = create(UploadPartCapabilitySchema, {
      partNumber: 1,
      sizeBytes: 1n,
      headers: [
        { name: 'Content-Length', value: '1' },
        { name: 'If-None-Match', value: '*' },
      ],
    });
    expect(uploadHeaders(part, file).has('content-length')).toBe(false);
    expect(uploadHeaders(part, file).get('if-none-match')).toBe('*');
    for (const name of ['cookie', 'authorization', 'x-forwarded-host'])
      expect(() =>
        uploadHeaders(
          { ...part, headers: [{ $typeName: 'files.v1.CapabilityHeader', name, value: 'secret' }] },
          file,
        ),
      ).toThrow();
    expect(() => uploadHeaders({ ...part, offsetBytes: 1n }, file)).toThrow();
  });
  it('never retries a mutation after a lost reply', async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new TypeError('lost reply'));
    const api = createAvatarApi('https://app.test', fetcher);
    await expect(api.clear(1n, '01'.repeat(16), new AbortController().signal)).rejects.toThrow();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it('downloads only a bounded READY derivative and verifies its digest without credentials', async () => {
    vi.stubGlobal('crypto', webcrypto);
    const data = new Uint8Array([137, 80, 78, 71]);
    const sha = new Uint8Array(await crypto.subtle.digest('SHA-256', data));
    let badHash = false;
    let oversized = false;
    const calls: Request[] = [];
    const fetcher = vi.fn<typeof fetch>(async (input, init) => {
      const req = new Request(input, init);
      calls.push(req);
      if (req.url.endsWith('/GetStatus'))
        return new Response(
          toBinary(
            GetStatusResponseSchema,
            create(GetStatusResponseSchema, {
              state: FileState.READY,
              cleanMediaType: 'image/png',
              cleanSizeBytes: 4n,
              cleanSha256: badHash ? new Uint8Array(32) : sha,
            }),
          ),
          { headers: { 'content-type': 'application/proto' } },
        );
      if (req.url.endsWith('/CreateDownload'))
        return new Response(
          toBinary(
            CreateDownloadResponseSchema,
            create(CreateDownloadResponseSchema, {
              url: 'https://storage.test/clean?signature=private',
              expiresAtUnix: BigInt(Math.floor(Date.now() / 1000) + 30),
            }),
          ),
          { headers: { 'content-type': 'application/proto' } },
        );
      return new Response(oversized ? new Uint8Array(5) : data, {
        headers: { 'content-type': 'image/png' },
      });
    });
    try {
      const api = createAvatarApi('https://app.test', fetcher, 'https://upload.test', [
        'https://storage.test',
      ]);
      const blob = await api.image(new Uint8Array(16).fill(1), new AbortController().signal);
      expect(blob.size).toBe(4);
      const request = calls.at(-1)!;
      expect(request.credentials).toBe('omit');
      expect(request.referrerPolicy).toBe('no-referrer');
      expect(request.redirect).toBe('error');
      badHash = true;
      await expect(
        api.image(new Uint8Array(16).fill(1), new AbortController().signal),
      ).rejects.toThrow();
      badHash = false;
      oversized = true;
      await expect(
        api.image(new Uint8Array(16).fill(1), new AbortController().signal),
      ).rejects.toThrow();
    } finally {
      vi.unstubAllGlobals();
    }
  });
  it('checks owner identity before returning a profile avatar', async () => {
    const a = create(AvatarSchema, { subjectId: new Uint8Array(16).fill(2), version: 1n });
    const fetcher = vi.fn<typeof fetch>(
      async () =>
        new Response(
          toBinary(GetAvatarResponseSchema, create(GetAvatarResponseSchema, { avatar: a })),
          { headers: { 'content-type': 'application/proto' } },
        ),
    );
    const api = createAvatarApi('https://app.test', fetcher);
    await expect(api.get('01'.repeat(16), new AbortController().signal)).rejects.toThrow();
  });
});
