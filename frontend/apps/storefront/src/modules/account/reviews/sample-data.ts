/** Образцовые данные до backend соответствующей возможности (MM-52…MM-55). */
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

const copy = <T>(value: T): T => structuredClone(value);
export async function loadSampleReviews(): Promise<{
  waiting: SampleWaitingReview[];
  mine: SampleReview[];
}> {
  return copy({ waiting: waitingReviews, mine: myReviews });
}
