import { create, toBinary } from '@bufbuild/protobuf';
import { ConnectError } from '@connectrpc/connect';
import {
  GetMyShopResponseSchema,
  ShopSchema,
  ShopStatus,
  SubmitApplicationRequestSchema,
} from '@marketmesh/api/seller/v1/seller_pb';
import { fromBinary } from '@bufbuild/protobuf';
import { describe, expect, it, vi } from 'vitest';
import { createSellerApi } from './api';

const shop = create(ShopSchema, {
  shopId: Uint8Array.from({ length: 16 }, () => 3),
  shopName: 'Глиняные истории',
  status: ShopStatus.APPROVED,
});
const okResponse = () =>
  new Response(toBinary(GetMyShopResponseSchema, create(GetMyShopResponseSchema, { shop })), {
    headers: { 'content-type': 'application/proto' },
  });

describe('seller API facade', () => {
  it('submits applications and reads the own shop over the one-origin transport', async () => {
    const requests: Request[] = [];
    const fetcher = vi.fn<typeof fetch>(async (input, init) => {
      requests.push(new Request(input, init));
      return okResponse();
    });
    const api = createSellerApi('https://seller.marketmesh.test', fetcher);
    await api.submitApplication({
      email: 'master@example.ru',
      password: new TextEncoder().encode('Secret123!'),
      shopName: 'Глиняные истории',
      inn: '7707083893',
    });
    const result = await api.getMyShop();
    expect(result.status).toBe('approved');
    expect(requests.map((r) => new URL(r.url).pathname)).toEqual([
      '/seller.v1.SellerService/SubmitApplication',
      '/seller.v1.SellerService/GetMyShop',
    ]);
    const input = fromBinary(
      SubmitApplicationRequestSchema,
      new Uint8Array(await requests[0]!.arrayBuffer()),
    );
    expect(input.inn).toBe('7707083893');
    expect(new TextDecoder().decode(input.password)).toBe('Secret123!');
  });

  it('rejects malformed shop responses', async () => {
    const broken = create(ShopSchema, { ...shop, shopId: new Uint8Array(16) });
    const fetcher = vi.fn<typeof fetch>(async () =>
      Promise.resolve(
        new Response(
          toBinary(GetMyShopResponseSchema, create(GetMyShopResponseSchema, { shop: broken })),
          { headers: { 'content-type': 'application/proto' } },
        ),
      ),
    );
    await expect(
      createSellerApi('https://seller.marketmesh.test', fetcher).getMyShop(),
    ).rejects.toBeInstanceOf(ConnectError);
  });
});
