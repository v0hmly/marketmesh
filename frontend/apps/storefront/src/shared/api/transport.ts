import { createConnectTransport } from '@connectrpc/connect-web';

/**
 * Общий транспорт публичных Connect-контрактов: приложение и шлюз делят один
 * HTTPS origin, cookies остаются HttpOnly и недоступны JavaScript.
 */
export function createPublicTransport(
  origin: string,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
) {
  const url = new URL(origin);
  if (url.protocol !== 'https:' || url.username || url.password || url.origin !== origin) {
    throw new Error('Для входа требуется HTTPS origin приложения');
  }
  return createConnectTransport({
    baseUrl: origin,
    useBinaryFormat: true,
    useHttpGet: false,
    defaultTimeoutMs: 15_000,
    fetch: (input, init) => {
      const requestURL = new URL(input instanceof Request ? input.url : String(input));
      const headers = new Headers(
        init?.headers ?? (input instanceof Request ? input.headers : undefined),
      );
      headers.delete('X-MarketMesh-Time-Zone');
      if (requestURL.origin === origin && requestURL.pathname.startsWith('/auth.v1.AuthService/')) {
        try {
          const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
          if (zone && zone.length <= 128) headers.set('X-MarketMesh-Time-Zone', zone);
        } catch {
          // Display preference is optional; Auth uses explicit UTC as a fallback.
        }
      }
      return fetcher(input, {
        ...init,
        headers,
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
      });
    },
  });
}
