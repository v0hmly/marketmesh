import { Code, ConnectError, createClient } from '@connectrpc/connect';
import {
  FileService,
  FileState,
  type CreateUploadRequest,
  type UploadPartCapability,
} from '@marketmesh/api/files/v1/files_pb';
import { UserService, type Avatar } from '@marketmesh/api/user/v1/user_pb';
import type { InjectionKey } from 'vue';
import { createPublicTransport } from '@marketmesh/browser-client/transport';

export { FileState };
export type { Avatar };
export const MAX_SOURCE = 5 * 1024 * 1024;
export const MAX_CLEAN = 20 * 1024 * 1024;
export const avatarApiKey: InjectionKey<AvatarApi> = Symbol('marketmesh-avatar-api');
export type AvatarApi = ReturnType<typeof createAvatarApi>;
export interface PreparedUpload {
  request: Omit<CreateUploadRequest, '$typeName'>;
  file: File;
}
export const idText = (id: Uint8Array) =>
  Array.from(id, (v) => v.toString(16).padStart(2, '0')).join('');
const validId = (id: Uint8Array) => id.length === 16 && id.some((v) => v !== 0);
function invalid(): never {
  throw new ConnectError('Invalid avatar response', Code.DataLoss);
}
export function validateAvatar(value: Avatar | undefined, subject: string): Avatar {
  if (
    !value ||
    !validId(value.subjectId) ||
    idText(value.subjectId) !== subject ||
    value.version < 1n ||
    value.version > 9223372036854775807n ||
    (value.fileId.length !== 0 && !validId(value.fileId))
  )
    invalid();
  return value;
}
export async function prepareUpload(file: File): Promise<PreparedUpload> {
  if (!['image/png', 'image/jpeg'].includes(file.type) || file.size < 1 || file.size > MAX_SOURCE)
    throw new ConnectError('Choose a bounded JPEG or PNG', Code.InvalidArgument);
  const bytes = await file.arrayBuffer();
  const head = new Uint8Array(bytes, 0, Math.min(8, bytes.byteLength));
  if (
    (file.type === 'image/png' && idText(head) !== '89504e470d0a1a0a') ||
    (file.type === 'image/jpeg' && (head[0] !== 255 || head[1] !== 216 || head[2] !== 255))
  )
    throw new ConnectError('Invalid raster signature', Code.InvalidArgument);
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes));
  const key = crypto.getRandomValues(new Uint8Array(16));
  return {
    file,
    request: {
      idempotencyKey: key,
      mediaType: file.type,
      sizeBytes: BigInt(file.size),
      sha256: digest,
      parts: [{ $typeName: 'files.v1.PartManifest', sizeBytes: BigInt(file.size), sha256: digest }],
    },
  };
}

export function storageURL(
  raw: string,
  origins: readonly string[],
  expires: bigint,
  now = Date.now(),
): string {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return invalid();
  }
  if (
    url.protocol !== 'https:' ||
    url.username ||
    url.password ||
    url.hash ||
    !origins.includes(url.origin) ||
    !url.search ||
    expires * 1000n <= BigInt(now) ||
    expires * 1000n > BigInt(now + 65_000)
  )
    invalid();
  return url.href;
}
const signedHeaders = new Set([
  'content-type',
  'content-length',
  'if-none-match',
  'x-amz-checksum-sha256',
  'x-amz-sdk-checksum-algorithm',
  'x-amz-server-side-encryption',
  'x-amz-server-side-encryption-aws-kms-key-id',
  'x-amz-meta-file-id',
  'x-amz-meta-upload-id',
]);
export function uploadHeaders(part: UploadPartCapability, file: File): Headers {
  if (part.partNumber !== 1 || part.offsetBytes !== 0n || part.sizeBytes !== BigInt(file.size))
    invalid();
  const result = new Headers();
  const seen = new Set<string>();
  for (const { name, value } of part.headers) {
    const key = name.toLowerCase();
    if (!signedHeaders.has(key) || seen.has(key) || /[\r\n]/.test(value)) invalid();
    seen.add(key);
    // Browsers set this forbidden header from the immutable Blob body.
    if (key === 'content-length') {
      if (value !== String(file.size)) invalid();
    } else result.set(key, value);
  }
  return result;
}
export function createAvatarApi(
  origin = window.location.origin,
  fetcher: typeof fetch = globalThis.fetch.bind(globalThis),
  uploadOrigin = import.meta.env.VITE_FILES_UPLOAD_ORIGIN ?? '',
  downloadOrigins = (import.meta.env.VITE_FILES_DOWNLOAD_ORIGINS ?? '').split(',').filter(Boolean),
) {
  const transport = createPublicTransport(origin, fetcher);
  const user = createClient(UserService, transport);
  const files = createClient(FileService, transport);
  const rpcOptions = (signal: AbortSignal) => ({ signal });
  return {
    async get(subject: string, signal: AbortSignal) {
      return validateAvatar((await user.getAvatar({}, rpcOptions(signal))).avatar, subject);
    },
    async set(fileId: Uint8Array, expectedVersion: bigint, subject: string, signal: AbortSignal) {
      return validateAvatar(
        (await user.setAvatar({ fileId, expectedVersion }, rpcOptions(signal))).avatar,
        subject,
      );
    },
    async clear(expectedVersion: bigint, subject: string, signal: AbortSignal) {
      return validateAvatar(
        (await user.clearAvatar({ expectedVersion }, rpcOptions(signal))).avatar,
        subject,
      );
    },
    async create(prepared: PreparedUpload, signal: AbortSignal) {
      const r = await files.createUpload(prepared.request, rpcOptions(signal));
      if (
        !validId(r.fileId) ||
        r.parts.length > 1 ||
        r.receivedParts.length > 1 ||
        r.receivedParts.some((p) => p !== 1) ||
        r.state === FileState.UNSPECIFIED ||
        !Object.values(FileState).includes(r.state)
      )
        invalid();
      return r;
    },
    async put(part: UploadPartCapability, file: File, signal: AbortSignal) {
      const url = storageURL(part.url, [uploadOrigin], part.expiresAtUnix);
      const response = await fetcher(url, {
        method: 'PUT',
        body: file,
        headers: uploadHeaders(part, file),
        credentials: 'omit',
        cache: 'no-store',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
        signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]),
      });
      if (!response.ok) throw new ConnectError('Upload not confirmed', Code.Unavailable);
    },
    complete(fileId: Uint8Array, signal: AbortSignal) {
      return files.completeUpload({ fileId }, rpcOptions(signal));
    },
    status(fileId: Uint8Array, signal: AbortSignal) {
      return files.getStatus({ fileId }, rpcOptions(signal));
    },
    remove(fileId: Uint8Array, signal: AbortSignal) {
      return files.delete({ fileId }, rpcOptions(signal));
    },
    async image(fileId: Uint8Array, signal: AbortSignal): Promise<Blob> {
      const status = await files.getStatus({ fileId }, rpcOptions(signal));
      if (
        status.state !== FileState.READY ||
        !['image/png', 'image/jpeg'].includes(status.cleanMediaType) ||
        status.cleanSizeBytes < 1n ||
        status.cleanSizeBytes > BigInt(MAX_CLEAN) ||
        status.cleanSha256.length !== 32
      )
        invalid();
      const cap = await files.createDownload({ fileId }, rpcOptions(signal));
      const url = storageURL(cap.url, downloadOrigins, cap.expiresAtUnix);
      const response = await fetcher(url, {
        credentials: 'omit',
        cache: 'no-store',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
        signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]),
      });
      if (
        !response.ok ||
        !response.body ||
        response.headers.get('content-type')?.split(';')[0] !== status.cleanMediaType
      )
        invalid();
      const reader = response.body.getReader();
      const chunks: Uint8Array<ArrayBuffer>[] = [];
      let size = 0;
      try {
        for (;;) {
          const part = await reader.read();
          if (part.done) break;
          size += part.value.byteLength;
          if (size > Number(status.cleanSizeBytes)) invalid();
          chunks.push(new Uint8Array(part.value));
        }
      } finally {
        await reader.cancel().catch(() => {});
        reader.releaseLock();
      }
      if (size !== Number(status.cleanSizeBytes)) invalid();
      const blob = new Blob(chunks, { type: status.cleanMediaType });
      const digest = new Uint8Array(
        await crypto.subtle.digest('SHA-256', await blob.arrayBuffer()),
      );
      if (idText(digest) !== idText(status.cleanSha256)) invalid();
      return blob;
    },
  };
}
