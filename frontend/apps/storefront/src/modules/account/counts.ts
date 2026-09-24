import { inject, provide, reactive, type InjectionKey } from 'vue';

/** Разделы кабинета, у которых в меню показывается счётчик. */
export type CountedSection = 'orders' | 'favorites' | 'reviews';
export type AccountCounts = Partial<Record<CountedSection, number>>;

const countsKey: InjectionKey<AccountCounts> = Symbol('marketmesh-account-counts');

/** Каркас кабинета владеет счётчиками меню и сбрасывает их при смене владельца. */
export function provideAccountCounts(): AccountCounts {
  const counts = reactive<AccountCounts>({});
  provide(countsKey, counts);
  return counts;
}

/**
 * Экран раздела сообщает актуальное число записей после загрузки или изменения.
 * Вне каркаса кабинета (например, в изолированном тесте экрана) счётчиков нет.
 */
export function useAccountCounts(): AccountCounts | null {
  return inject(countsKey, null);
}
