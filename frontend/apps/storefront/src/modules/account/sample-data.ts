/**
 * Временные образцовые данные разделов кабинета до появления backend API
 * (эпики MM-52…MM-55). Не экспортировать за пределы modules/account:
 * после подключения API файл удаляется вместе с этими загрузчиками.
 */

export type SampleOrderStatus = 'shipping' | 'ready' | 'done' | 'cancelled';

export interface SampleOrderLine {
  title: string;
  meta: string;
  amount: string;
}

export interface SampleOrder {
  id: string;
  number: string;
  placed: string;
  total: string;
  status: SampleOrderStatus;
  /** Шаг трекера для активных заказов: 0 — собран, 3 — готов к получению. */
  step?: number;
  code?: string;
  delivery: string;
  items: SampleOrderLine[];
}

export interface SampleFavorite {
  id: string;
  title: string;
  maker: string;
  price: string;
  left: number;
}

export interface SampleWaitingReview {
  id: string;
  title: string;
  meta: string;
}

export interface SampleReview {
  id: string;
  title: string;
  rating: number;
  date: string;
  status: 'Опубликован' | 'На модерации';
  text: string;
  reply: string;
}

const orders: SampleOrder[] = [
  {
    id: 'o1',
    number: '№ 1482-0091',
    placed: 'ОФОРМЛЕН 18 СЕНТЯБРЯ',
    total: '7 500 ₽',
    status: 'shipping',
    step: 2,
    delivery:
      'Доставка до двери · Санкт-Петербург, набережная реки Мойки, 12 · ожидается 24 сентября',
    items: [
      {
        title: 'Льняное полотенце с мережкой',
        meta: 'Мастерская «Тихий лён» · 2 шт.',
        amount: '3 600 ₽',
      },
      {
        title: 'Шарф крупной вязки',
        meta: 'Мастерская «Северные петли» · 1 шт.',
        amount: '3 900 ₽',
      },
    ],
  },
  {
    id: 'o2',
    number: '№ 1471-0088',
    placed: 'ОФОРМЛЕН 12 СЕНТЯБРЯ',
    total: '2 400 ₽',
    status: 'ready',
    step: 3,
    code: '481 902',
    delivery: 'Пункт выдачи · Санкт-Петербург, Гороховая, 38 · хранится до 26 сентября',
    items: [
      {
        title: 'Кружка «Пена», объём 300 мл',
        meta: 'Мастерская «Глина и соль» · 1 шт.',
        amount: '2 400 ₽',
      },
    ],
  },
  {
    id: 'o3',
    number: '№ 1402-0075',
    placed: 'ПОЛУЧЕН 29 АВГУСТА',
    total: '5 900 ₽',
    status: 'done',
    delivery: 'Получен в пункте выдачи · Санкт-Петербург, Гороховая, 38',
    items: [
      {
        title: 'Набор открыток «Север»',
        meta: 'Мастерская «Бумага и снег» · 1 шт.',
        amount: '900 ₽',
      },
      {
        title: 'Плед из шерсти мериноса',
        meta: 'Мастерская «Северные петли» · 1 шт.',
        amount: '5 000 ₽',
      },
    ],
  },
  {
    id: 'o4',
    number: '№ 1388-0064',
    placed: 'ОТМЕНЁН 14 АВГУСТА',
    total: '1 800 ₽',
    status: 'cancelled',
    delivery: 'Отменён покупателем · деньги возвращены 15 августа',
    items: [
      {
        title: 'Керамическая тарелка «Волна»',
        meta: 'Мастерская «Глина и соль» · 1 шт.',
        amount: '1 800 ₽',
      },
    ],
  },
];

const favorites: SampleFavorite[] = [
  {
    id: 'f1',
    title: 'Керамическая тарелка «Волна»',
    maker: 'Мастерская «Глина и соль»',
    price: '1 800 ₽',
    left: 4,
  },
  {
    id: 'f2',
    title: 'Плед из шерсти мериноса',
    maker: 'Мастерская «Северные петли»',
    price: '9 400 ₽',
    left: 1,
  },
  {
    id: 'f3',
    title: 'Свеча «Хвоя и дым», 200 мл',
    maker: 'Мастерская «Тёплый воск»',
    price: '1 200 ₽',
    left: 0,
  },
  {
    id: 'f4',
    title: 'Льняная скатерть, 140 × 220',
    maker: 'Мастерская «Тихий лён»',
    price: '6 700 ₽',
    left: 7,
  },
  {
    id: 'f5',
    title: 'Деревянная доска из дуба',
    maker: 'Мастерская «Слой»',
    price: '3 300 ₽',
    left: 0,
  },
  {
    id: 'f6',
    title: 'Набор открыток «Север»',
    maker: 'Мастерская «Бумага и снег»',
    price: '900 ₽',
    left: 12,
  },
];

const waitingReviews: SampleWaitingReview[] = [
  {
    id: 'w1',
    title: 'Плед из шерсти мериноса',
    meta: 'Мастерская «Северные петли» · получен 29 августа',
  },
  {
    id: 'w2',
    title: 'Набор открыток «Север»',
    meta: 'Мастерская «Бумага и снег» · получен 29 августа',
  },
];

const myReviews: SampleReview[] = [
  {
    id: 'm1',
    title: 'Кружка «Пена», объём 300 мл',
    rating: 5,
    date: '2 СЕНТЯБРЯ',
    status: 'Опубликован',
    text: 'Глазурь ровная, ручка удобно ложится в ладонь. Пришла в плотной коробке с бумагой, ни одного скола.',
    reply: 'Спасибо! Партию такой глазури повторим в октябре.',
  },
  {
    id: 'm2',
    title: 'Льняное полотенце с мережкой',
    rating: 4,
    date: '19 АВГУСТА',
    status: 'Опубликован',
    text: 'Лён плотный, после стирки стал мягче. Мережка ровная, но цвет чуть темнее, чем на фотографии.',
    reply: '',
  },
  {
    id: 'm3',
    title: 'Свеча «Хвоя и дым», 200 мл',
    rating: 5,
    date: '3 АВГУСТА',
    status: 'На модерации',
    text: 'Запах не приторный, горит ровно около сорока часов. Беру второй раз.',
    reply: '',
  },
];

/** Каждая загрузка возвращает копию, чтобы экран мог изменять свой список локально. */
const copy = <T>(value: T): T => structuredClone(value);

export async function loadSampleOrders(): Promise<SampleOrder[]> {
  return copy(orders);
}

export async function loadSampleFavorites(): Promise<SampleFavorite[]> {
  return copy(favorites);
}

export async function loadSampleReviews(): Promise<{
  waiting: SampleWaitingReview[];
  mine: SampleReview[];
}> {
  return copy({ waiting: waitingReviews, mine: myReviews });
}
