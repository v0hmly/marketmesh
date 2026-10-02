/**
 * Временные образцовые данные витрины до появления API каталога, корзины и
 * обратной связи (эпики MM-52 … MM-55). Не экспортировать за пределы
 * modules/catalog: после подключения API файл удаляется вместе с загрузчиками.
 */

export type SampleCategory = 'clothes' | 'ceramics' | 'textile' | 'wood' | 'candles' | 'paper';

export interface SampleBatch {
  id: string;
  title: string;
  shop: string;
  /** Цена в рублях. */
  price: number;
  /** Сколько изделий осталось в партии. */
  left: number;
  /** Размер партии. */
  batch: number;
  category: SampleCategory;
  /** Дата публикации партии, ISO. */
  published: string;
}

export const sampleCategories: { value: SampleCategory; label: string }[] = [
  { value: 'clothes', label: 'Одежда' },
  { value: 'ceramics', label: 'Керамика' },
  { value: 'textile', label: 'Текстиль' },
  { value: 'wood', label: 'Дерево' },
  { value: 'candles', label: 'Свечи' },
  { value: 'paper', label: 'Бумага' },
];

const batches: SampleBatch[] = [
  {
    id: 'p1',
    title: 'Футболка с машинной вышивкой «Сойка»',
    shop: 'Мастерская «Нитка и игла»',
    price: 2900,
    left: 8,
    batch: 10,
    category: 'clothes',
    published: '2026-09-30',
  },
  {
    id: 'p2',
    title: 'Кружка «Пена», 300 мл',
    shop: 'Мастерская «Глина и соль»',
    price: 2400,
    left: 5,
    batch: 12,
    category: 'ceramics',
    published: '2026-09-30',
  },
  {
    id: 'p3',
    title: 'Шарф крупной вязки, меринос',
    shop: 'Мастерская «Северные петли»',
    price: 5900,
    left: 2,
    batch: 6,
    category: 'clothes',
    published: '2026-09-29',
  },
  {
    id: 'p4',
    title: 'Льняная скатерть, 140 × 220',
    shop: 'Мастерская «Тихий лён»',
    price: 6700,
    left: 7,
    batch: 7,
    category: 'textile',
    published: '2026-09-29',
  },
  {
    id: 'p5',
    title: 'Свеча «Хвоя и дым», 200 мл',
    shop: 'Мастерская «Тёплый воск»',
    price: 1200,
    left: 18,
    batch: 20,
    category: 'candles',
    published: '2026-09-28',
  },
  {
    id: 'p6',
    title: 'Разделочная доска из дуба',
    shop: 'Мастерская «Слой»',
    price: 3300,
    left: 4,
    batch: 5,
    category: 'wood',
    published: '2026-09-28',
  },
  {
    id: 'p7',
    title: 'Набор открыток «Север», 6 шт.',
    shop: 'Мастерская «Бумага и снег»',
    price: 900,
    left: 30,
    batch: 40,
    category: 'paper',
    published: '2026-09-27',
  },
  {
    id: 'p8',
    title: 'Керамическая тарелка «Волна»',
    shop: 'Мастерская «Глина и соль»',
    price: 1800,
    left: 0,
    batch: 8,
    category: 'ceramics',
    published: '2026-09-26',
  },
  {
    id: 'p9',
    title: 'Худи с вышивкой «Карельский лес»',
    shop: 'Мастерская «Нитка и игла»',
    price: 5400,
    left: 6,
    batch: 6,
    category: 'clothes',
    published: '2026-09-26',
  },
  {
    id: 'p10',
    title: 'Миска «Туман», 600 мл',
    shop: 'Мастерская «Глина и соль»',
    price: 2100,
    left: 9,
    batch: 10,
    category: 'ceramics',
    published: '2026-09-25',
  },
  {
    id: 'p11',
    title: 'Льняные салфетки, 4 шт.',
    shop: 'Мастерская «Тихий лён»',
    price: 1900,
    left: 14,
    batch: 15,
    category: 'textile',
    published: '2026-09-25',
  },
  {
    id: 'p12',
    title: 'Подсвечник из ясеня',
    shop: 'Мастерская «Слой»',
    price: 1600,
    left: 3,
    batch: 8,
    category: 'wood',
    published: '2026-09-24',
  },
];

export const sampleOrders: { value: string; label: string }[] = [
  { value: '1482-0091', label: '№ 1482-0091 · 18 сентября · 7 500 ₽' },
  { value: '1471-0088', label: '№ 1471-0088 · 12 сентября · 2 400 ₽' },
  { value: '1402-0075', label: '№ 1402-0075 · 29 августа · 5 900 ₽' },
];

/** Новые партии за последнюю неделю, от новых к старым. */
export async function loadSampleBatches(): Promise<SampleBatch[]> {
  return batches.map((item) => ({ ...item }));
}

/** Отправка обращения: возвращает номер обращения. */
export async function sendSampleFeedback(): Promise<string> {
  await new Promise((resolve) => setTimeout(resolve, 400));
  return '48213';
}
