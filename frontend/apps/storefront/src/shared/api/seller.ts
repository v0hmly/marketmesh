import { Code, ConnectError, createClient } from '@connectrpc/connect';
import {
  SellerService,
  ShopStatus,
  type Shop as WireShop,
} from '@marketmesh/api/seller/v1/seller_pb';
import { createPublicTransport } from './transport';

export interface Shop {
  shopId: Uint8Array;
  shopName: string;
  status: 'pending' | 'approved' | 'rejected' | 'blocked';
  rejectReason: string;
  fixInstructions: string;
  createdAtUnix: bigint;
}

/** Only browser-public seller RPCs; cookies are managed exclusively by the browser. */
export interface SellerApi {
  submitApplication(input: {
    email: string;
    password: Uint8Array;
    shopName: string;
    inn: string;
  }): Promise<void>;
  getMyShop(): Promise<Shop>;
}

const wireStatuses = {
  [ShopStatus.PENDING]: 'pending',
  [ShopStatus.APPROVED]: 'approved',
  [ShopStatus.REJECTED]: 'rejected',
  [ShopStatus.BLOCKED]: 'blocked',
} as const;

function validShop(shop: WireShop | undefined): Shop {
  if (
    !shop ||
    shop.shopId.length !== 16 ||
    shop.shopId.every((v) => v === 0) ||
    !shop.shopName ||
    !(shop.status in wireStatuses) ||
    (shop.status !== ShopStatus.REJECTED && (shop.rejectReason || shop.fixInstructions))
  ) {
    throw new ConnectError('Invalid shop response', Code.DataLoss);
  }
  return {
    shopId: shop.shopId,
    shopName: shop.shopName,
    status: wireStatuses[shop.status as keyof typeof wireStatuses],
    rejectReason: shop.rejectReason,
    fixInstructions: shop.fixInstructions,
    createdAtUnix: shop.createdAtUnix,
  };
}

/** Фасад области продавца поверх сгенерированного seller.v1 клиента (MM-81). */
export function createSellerApi(
  origin = window.location.origin,
  fetcher: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
): SellerApi {
  const client = createClient(SellerService, createPublicTransport(origin, fetcher));
  return {
    async submitApplication(input) {
      await client.submitApplication(input);
    },
    async getMyShop() {
      return validShop((await client.getMyShop({})).shop);
    },
  };
}
