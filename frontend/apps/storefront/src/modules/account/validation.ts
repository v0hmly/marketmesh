import { trimDisplayName, wellFormed } from '../../shared/validation';

const utf8 = new TextEncoder();
const controls = /\p{Cc}/u;

export function validateProfile(
  displayName: string,
  bio: string,
): { displayName?: string; bio?: string } {
  const errors: { displayName?: string; bio?: string } = {};
  const normalized = trimDisplayName(displayName);
  if (
    !wellFormed(displayName) ||
    utf8.encode(displayName).length > 320 ||
    Array.from(normalized).length > 80 ||
    controls.test(normalized)
  ) {
    errors.displayName = 'Имя: не более 80 символов и 320 байт UTF-8, без управляющих символов.';
  }
  if (
    !wellFormed(bio) ||
    utf8.encode(bio).length > 4000 ||
    Array.from(bio).length > 1000 ||
    controls.test(bio.replace(/[\n\t]/g, ''))
  ) {
    errors.bio =
      'О себе: не более 1000 символов и 4000 байт UTF-8. Допустимы переносы строк и табуляция.';
  }
  return errors;
}

const datePattern = /^(\d{4})-(\d{2})-(\d{2})$/;

export interface IdentityDraft {
  displayName: string;
  lastName: string;
  birthDate: string;
  phone: string;
  city: string;
}

export interface IdentityErrors {
  displayName?: string;
  lastName?: string;
  birthDate?: string;
  phone?: string;
  city?: string;
}

/** Личные данные MarketMesh ID; правила согласованы с user.v1.Profile. */
export function validateIdentity(draft: IdentityDraft): IdentityErrors {
  const errors: IdentityErrors = {};
  const name = trimDisplayName(draft.displayName);
  if (!name) {
    errors.displayName = 'Введите имя — оно нужно мастеру и видно в ваших отзывах.';
  } else if (
    !wellFormed(draft.displayName) ||
    utf8.encode(draft.displayName).length > 320 ||
    Array.from(name).length > 80 ||
    controls.test(name)
  ) {
    errors.displayName = 'Имя: не более 80 символов и 320 байт UTF-8, без управляющих символов.';
  }
  if (Array.from(trimDisplayName(draft.lastName)).length > 80)
    errors.lastName = 'Фамилия: не более 80 символов.';
  if (draft.birthDate) {
    const match = datePattern.exec(draft.birthDate);
    const date = match ? new Date(`${draft.birthDate}T00:00:00`) : null;
    if (!match || !date || Number.isNaN(date.getTime()))
      errors.birthDate = 'Дата указывается в формате ГГГГ-ММ-ДД.';
    else if (date.getTime() > Date.now())
      errors.birthDate = 'Проверьте дату: она не может быть в будущем.';
  }
  if (draft.phone) {
    const digits = draft.phone.replace(/\D/g, '');
    if (digits.length < 7 || digits.length > 15)
      errors.phone = 'Проверьте номер: нужно от 7 до 15 цифр.';
  }
  if (Array.from(trimDisplayName(draft.city)).length > 120)
    errors.city = 'Город: не более 120 символов.';
  return errors;
}

/** Возраст в полных годах из даты YYYY-MM-DD; null, если дата пустая или неправдоподобная. */
export function ageOf(birthDate: string): number | null {
  const match = datePattern.exec(birthDate);
  if (!match) return null;
  const now = new Date();
  let age = now.getFullYear() - Number(match[1]);
  const month = now.getMonth() + 1 - Number(match[2]);
  if (month < 0 || (month === 0 && now.getDate() < Number(match[3]))) age -= 1;
  return age >= 0 && age < 130 ? age : null;
}

export function yearWord(age: number): string {
  const tail = age % 10;
  const hundred = age % 100;
  if (hundred >= 11 && hundred <= 14) return 'лет';
  if (tail === 1) return 'год';
  if (tail >= 2 && tail <= 4) return 'года';
  return 'лет';
}

const monthNames = [
  'января',
  'февраля',
  'марта',
  'апреля',
  'мая',
  'июня',
  'июля',
  'августа',
  'сентября',
  'октября',
  'ноября',
  'декабря',
];

/** Дата YYYY-MM-DD словами: «12 марта 1989 года». */
export function birthLabel(birthDate: string): string {
  const match = datePattern.exec(birthDate);
  if (!match) return 'Не указана';
  return `${Number(match[3])} ${monthNames[Number(match[2]) - 1]} ${match[1]} года`;
}
