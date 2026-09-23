import { trimDisplayName, wellFormed } from '../../shared/validation';

const utf8 = new TextEncoder();
const controls = /\p{Cc}/u;

const datePattern = /^(\d{4})-(\d{2})-(\d{2})$/;

function calendarDate(value: string): RegExpExecArray | null {
  const match = datePattern.exec(value);
  if (!match || Number(match[1]) < 1) return null;
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === value
    ? match
    : null;
}

function boundedText(value: string, limit: number): boolean {
  return (
    wellFormed(value) &&
    utf8.encode(value).length <= limit * 4 &&
    Array.from(trimDisplayName(value)).length <= limit &&
    !controls.test(value)
  );
}

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
  } else if (!boundedText(draft.displayName, 80)) {
    errors.displayName = 'Имя: не более 80 символов и 320 байт UTF-8, без управляющих символов.';
  }
  if (!boundedText(draft.lastName, 80))
    errors.lastName = 'Фамилия: не более 80 символов и 320 байт UTF-8, без управляющих символов.';
  if (draft.birthDate) {
    if (!calendarDate(draft.birthDate))
      errors.birthDate = 'Укажите существующую дату в формате ГГГГ-ММ-ДД.';
    else if (draft.birthDate > new Date().toISOString().slice(0, 10))
      errors.birthDate = 'Проверьте дату: она не может быть в будущем.';
  }
  if (draft.phone) {
    const digits = draft.phone.replace(/\D/g, '');
    if (
      draft.phone.length > 32 ||
      /[^0-9 +.()-]/.test(draft.phone) ||
      (draft.phone.trim() !== '' && (digits.length < 7 || digits.length > 15))
    )
      errors.phone = 'Номер: от 7 до 15 цифр, не более 32 знаков. Допустимы пробелы и +-.().';
  }
  if (!boundedText(draft.city, 120))
    errors.city = 'Город: не более 120 символов и 480 байт UTF-8, без управляющих символов.';
  return errors;
}

/** Возраст в полных годах из даты YYYY-MM-DD; null, если дата пустая или неправдоподобная. */
export function ageOf(birthDate: string): number | null {
  const match = calendarDate(birthDate);
  if (!match) return null;
  const now = new Date();
  let age = now.getUTCFullYear() - Number(match[1]);
  const month = now.getUTCMonth() + 1 - Number(match[2]);
  if (month < 0 || (month === 0 && now.getUTCDate() < Number(match[3]))) age -= 1;
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

/** Публичная строка рядом с отзывом: имя, город и возраст, если его разрешено показывать. */
export function publicLineOf(identity: {
  displayName: string;
  city: string;
  birthDate: string;
  showAge: boolean;
}): string {
  const parts = [identity.displayName.trim(), identity.city.trim()];
  const age = identity.showAge ? ageOf(identity.birthDate) : null;
  if (age !== null) parts.push(`${age} ${yearWord(age)}`);
  return parts.filter(Boolean).join(', ') || 'Покупатель MarketMesh';
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
  const match = calendarDate(birthDate);
  if (!match) return 'Не указана';
  return `${Number(match[3])} ${monthNames[Number(match[2]) - 1]} ${match[1]} года`;
}
