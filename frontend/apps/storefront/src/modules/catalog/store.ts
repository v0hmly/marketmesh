import { reactive } from 'vue';

/**
 * Общее состояние витрины: шапка (поиск, корзина) и лента — соседние виды
 * маршрута, поэтому делят одно состояние модуля. Корзина гостя хранится в
 * этом браузере, пока нет API корзины (MM-55).
 */
export type StorefrontDialog = { kind: 'cart' } | { kind: 'signIn'; reason: string } | null;

const cartKey = 'marketmesh.storefront.cart';

function readCart(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(cartKey) ?? '[]');
    return Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

function writeCart(ids: string[]) {
  try {
    localStorage.setItem(cartKey, JSON.stringify(ids));
  } catch {
    /* Хранилище недоступно: корзина живёт до перезагрузки страницы. */
  }
}

const state = reactive({
  /** Применённый поисковый запрос; черновик живёт в поле шапки. */
  query: '',
  /** Сообщение о последнем действии над витриной. */
  notice: '',
  cart: readCart(),
  dialog: null as StorefrontDialog,
});

export function useStorefront() {
  return {
    state,
    search(query: string) {
      state.query = query.trim();
      state.notice = '';
    },
    addToCart(id: string) {
      if (state.cart.includes(id)) return false;
      state.cart = [...state.cart, id];
      writeCart(state.cart);
      return true;
    },
    removeFromCart(id: string) {
      state.cart = state.cart.filter((item) => item !== id);
      writeCart(state.cart);
    },
    openCart() {
      state.dialog = { kind: 'cart' };
    },
    askToSignIn(reason: string) {
      state.dialog = { kind: 'signIn', reason };
    },
    closeDialog() {
      state.dialog = null;
    },
    announce(text: string) {
      state.notice = text;
    },
  };
}

export function money(value: number) {
  return `${value.toLocaleString('ru-RU')} ₽`;
}

export function stockText(left: number, batch: number) {
  if (left === 0) return 'Партия распродана';
  if (left <= 2) return `Осталось ${left} из ${batch}`;
  return `В партии ${left} из ${batch}`;
}
