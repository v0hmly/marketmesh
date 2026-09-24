/** Образцовые данные до backend соответствующей возможности (MM-52…MM-55). */
export interface SampleFavorite {
  id: string;
  title: string;
  maker: string;
  price: string;
  left: number;
}

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

const copy = <T>(value: T): T => structuredClone(value);
export async function loadSampleFavorites(): Promise<SampleFavorite[]> {
  return copy(favorites);
}
