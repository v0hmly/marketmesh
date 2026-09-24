import { inject, type InjectionKey } from 'vue';
import { createSellerApi, type SellerApi } from './api';
export const sellerApiKey: InjectionKey<SellerApi> = Symbol('marketmesh-seller');
export function useSellerApi(): SellerApi {
  const value = inject(sellerApiKey, null);
  return value ?? createSellerApi();
}
