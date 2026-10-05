import { markRaw, nextTick, reactive } from 'vue';

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
  /** Текст постоянного live-региона: последнее действие над витриной для скринридера. */
  notice: '',
  /** Состояние оформления, показываемое внутри корзины, а не на странице. */
  cartNotice: '',
  cart: readCart(),
  dialog: null as StorefrontDialog,
  /** Куда вернуть фокус после диалогов: окно входа из корзины возвращает к кнопке корзины. */
  returnFocus: null as HTMLElement | null,
});

function rememberTrigger() {
  if (state.dialog) return;
  state.returnFocus =
    document.activeElement instanceof HTMLElement ? markRaw(document.activeElement) : null;
}

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
    /** Убирает из корзины изделия, которых больше нет на витрине. */
    retainInCart(known: string[]) {
      const kept = state.cart.filter((id) => known.includes(id));
      if (kept.length === state.cart.length) return;
      state.cart = kept;
      writeCart(kept);
    },
    removeFromCart(id: string) {
      state.cart = state.cart.filter((item) => item !== id);
      writeCart(state.cart);
      // Пустой корзине нечего оформлять.
      if (!state.cart.length) state.cartNotice = '';
    },
    openCart() {
      rememberTrigger();
      state.dialog = { kind: 'cart' };
    },
    askToSignIn(reason: string) {
      rememberTrigger();
      state.dialog = { kind: 'signIn', reason };
    },
    closeDialog() {
      state.dialog = null;
      state.cartNotice = '';
    },
    /**
     * Сообщает о действии в live-регион для скринридера; видимо действие подтверждает сама
     * кнопка карточки (MM-130), сетку ничто не раздвигает. Повтор объявляется заново.
     */
    announce(text: string) {
      state.notice = '';
      if (text)
        void nextTick(() => {
          state.notice = text;
        });
    },
    /** Ответ внутри корзины; как и announce, повтор того же текста объявляется заново. */
    setCartNotice(text: string) {
      state.cartNotice = '';
      if (text)
        void nextTick(() => {
          state.cartNotice = text;
        });
    },
  };
}

/** Название в кавычках «…»; кавычки внутри названия становятся „…“. */
export function quoted(title: string) {
  return `«${title.replace(/«/g, '„').replace(/»/g, '“')}»`;
}

export function money(value: number) {
  return `${value.toLocaleString('ru-RU')} ₽`;
}

/**
 * Партия заканчивается: два изделия или меньше и не больше трети партии, чтобы
 * маленькая партия («2 из 3») не горела охрой всегда. Только тогда шкала охряная.
 */
export function lowStock(left: number, batch: number) {
  return left > 0 && left <= 2 && left * 3 <= batch;
}

const batchDay = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long' });

/** Дата партии — отдельная строка бирки под шкалой (MM-133): «партия от 24 сентября». */
export function batchDateText(published: string) {
  return `партия от ${batchDay.format(new Date(`${published}T12:00:00`))}`;
}

/** Мастерская на плитке — только имя: «Мастерская «Слой»» → «Слой». */
export function shopName(shop: string) {
  return shop.replace(/^Мастерская\s+/, '').replace(/^«(.*)»$/, '$1');
}

/** Неразрывные пробелы, чтобы единицы не отрывались от чисел: «300 мл», «140 × 220». */
export function keepUnits(title: string) {
  return title
    .replace(/(\d)\s+×\s+(\d)/g, '$1\u00a0×\u00a0$2')
    .replace(/(\d)\s+(?=(мл|л|шт\.|см|мм|г|кг)(?![а-яё]))/gi, '$1\u00a0');
}

export function stockText(left: number, batch: number) {
  if (left === 0) return 'Партия распродана';
  if (left <= 2) return `Осталось ${left} из ${batch}`;
  return `В партии ${left} из ${batch}`;
}
