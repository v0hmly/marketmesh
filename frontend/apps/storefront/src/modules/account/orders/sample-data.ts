/** Образцовые данные до backend соответствующей возможности (MM-52…MM-55). */
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

const copy = <T>(value: T): T => structuredClone(value);
export async function loadSampleOrders(): Promise<SampleOrder[]> {
  return copy(orders);
}
