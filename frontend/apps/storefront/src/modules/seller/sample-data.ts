/**
 * Временные образцовые данные портала продавца до появления backend API
 * (эпики MM-52/MM-53/MM-55). Не экспортировать за пределы modules/seller:
 * после подключения API файл удаляется вместе с этими загрузчиками.
 */

export interface SellerTask {
  id: string;
  title: string;
  meta: string;
  action: string;
  message: string;
}

export interface SellerShopSummary {
  name: string;
  statusLabel: string;
  page: string;
  publishedCount: number;
  nextPayout: string;
}

export interface SellerLowStock {
  id: string;
  title: string;
  left: string;
}

export type SellerProductStatus = 'published' | 'moderation' | 'draft' | 'hidden';

export interface SellerProduct {
  id: string;
  title: string;
  variant: string;
  price: string;
  available: number;
  reserved: number;
  status: SellerProductStatus;
  note: string;
}

export type SellerOrderStatus = 'new' | 'packing' | 'shipped' | 'done' | 'cancelled';

export interface SellerOrderLine {
  title: string;
  amount: string;
}

export interface SellerOrder {
  id: string;
  number: string;
  placed: string;
  status: SellerOrderStatus;
  recipient: string;
  delivery: string;
  total: string;
  items: SellerOrderLine[];
}

const shop: SellerShopSummary = {
  name: 'Мастерская «Глина и соль»',
  statusLabel: 'Опубликован',
  page: 'marketmesh.ru/shop/glina-i-sol',
  publishedCount: 12,
  nextPayout: '30 сентября',
};

const tasks: SellerTask[] = [
  {
    id: 't1',
    title: 'Заказ № 1501-0102 ждёт подтверждения',
    meta: 'Оформлен 20 сентября · 2 изделия',
    action: 'Подтвердить',
    message: 'Заказ подтверждён. Следующий шаг — сборка.',
  },
  {
    id: 't2',
    title: 'Заказ № 1499-0099 собран и ждёт передачи',
    meta: 'Подтверждён 19 сентября · 1 изделие',
    action: 'Передать',
    message: 'Заказ передан в доставку.',
  },
  {
    id: 't3',
    title: 'Карточка «Кружка «Пена»» на модерации',
    meta: 'Отправлена 18 сентября',
    action: 'Открыть',
    message: 'Карточка открыта в разделе изделий.',
  },
];

const lowStock: SellerLowStock[] = [
  { id: 's1', title: 'Шарф крупной вязки', left: 'осталось 2' },
  { id: 's2', title: 'Кружка «Пена», объём 300 мл', left: 'осталось 1' },
];

const products: SellerProduct[] = [
  {
    id: 'p1',
    title: 'Кружка «Пена», объём 300 мл',
    variant: 'Глазурь белая матовая · керамика ручной работы',
    price: '2 400 ₽',
    available: 1,
    reserved: 2,
    status: 'moderation',
    note: 'На модерации с 18 сентября. Публикация после проверки.',
  },
  {
    id: 'p2',
    title: 'Шарф крупной вязки',
    variant: 'Шерсть меринос · два цвета',
    price: '5 900 ₽',
    available: 2,
    reserved: 0,
    status: 'published',
    note: '',
  },
  {
    id: 'p3',
    title: 'Льняное полотенце с мережкой',
    variant: 'Лён 240 г/м² · 50 × 70 см',
    price: '1 800 ₽',
    available: 12,
    reserved: 3,
    status: 'published',
    note: '',
  },
  {
    id: 'p4',
    title: 'Набор открыток «Север»',
    variant: 'Шесть штук · печать на хлопковой бумаге',
    price: '',
    available: 0,
    reserved: 0,
    status: 'draft',
    note: 'Не хватает цены и фотографий — карточку нельзя опубликовать.',
  },
];

const orders: SellerOrder[] = [
  {
    id: 'o1',
    number: '№ 1501-0102',
    placed: 'ОФОРМЛЕН 20 СЕНТЯБРЯ',
    status: 'new',
    recipient: 'Вера И.',
    delivery: 'Доставка до двери',
    total: '4 200 ₽',
    items: [
      { title: 'Льняное полотенце с мережкой × 2', amount: '3 600 ₽' },
      { title: 'Набор открыток «Север»', amount: '600 ₽' },
    ],
  },
  {
    id: 'o2',
    number: '№ 1499-0099',
    placed: 'ОФОРМЛЕН 19 СЕНТЯБРЯ',
    status: 'packing',
    recipient: 'Илья И.',
    delivery: 'Пункт выдачи',
    total: '5 900 ₽',
    items: [{ title: 'Шарф крупной вязки', amount: '5 900 ₽' }],
  },
  {
    id: 'o3',
    number: '№ 1471-0088',
    placed: 'ОФОРМЛЕН 12 СЕНТЯБРЯ',
    status: 'done',
    recipient: 'Анна К.',
    delivery: 'Пункт выдачи',
    total: '2 400 ₽',
    items: [{ title: 'Кружка «Пена», объём 300 мл', amount: '2 400 ₽' }],
  },
];

/** Каждая загрузка возвращает копию, чтобы экран мог изменять свой список локально. */
const copy = <T>(value: T): T => structuredClone(value);

export async function loadSellerOverview(): Promise<{
  shop: SellerShopSummary;
  tasks: SellerTask[];
  lowStock: SellerLowStock[];
}> {
  return copy({ shop, tasks, lowStock });
}

export async function loadSellerProducts(): Promise<SellerProduct[]> {
  return copy(products);
}

export async function loadSellerOrders(): Promise<SellerOrder[]> {
  return copy(orders);
}
