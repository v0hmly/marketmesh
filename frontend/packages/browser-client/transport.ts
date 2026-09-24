import { createConnectTransport } from "@connectrpc/connect-web";

/**
 * Общий транспорт публичных Connect-контрактов: приложение и шлюз делят один
 * HTTPS origin, cookies остаются HttpOnly и недоступны JavaScript.
 */
export function createPublicTransport(
  origin: string,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
) {
  const url = new URL(origin);
  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    url.origin !== origin
  ) {
    throw new Error("Для входа требуется HTTPS origin приложения");
  }
  return createConnectTransport({
    baseUrl: origin,
    useBinaryFormat: true,
    useHttpGet: false,
    defaultTimeoutMs: 15_000,
    fetch: (input, init) => {
      return fetcher(input, {
        ...init,
        credentials: "same-origin",
        cache: "no-store",
        redirect: "error",
        referrerPolicy: "no-referrer",
      });
    },
  });
}
