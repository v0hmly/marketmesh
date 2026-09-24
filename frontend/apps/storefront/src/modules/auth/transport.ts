import { createPublicTransport } from '@marketmesh/browser-client/transport';
/** Auth owns the optional IANA display preference used in security emails. */
export function createAuthTransport(
  origin: string,
  fetcher: typeof fetch = globalThis.fetch.bind(globalThis),
) {
  return createPublicTransport(origin, (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    const headers = new Headers(
      init?.headers ?? (input instanceof Request ? input.headers : undefined),
    );
    headers.delete('X-MarketMesh-Time-Zone');
    if (url.origin === origin && url.pathname.startsWith('/auth.v1.AuthService/')) {
      try {
        const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
        if (zone && zone.length <= 128) headers.set('X-MarketMesh-Time-Zone', zone);
      } catch {
        /* Auth uses UTC when the display preference is unavailable. */
      }
    }
    return fetcher(input, { ...init, headers });
  });
}
