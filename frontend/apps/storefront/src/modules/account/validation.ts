const utf8 = new TextEncoder();
const whitespaceEdges = /^\p{White_Space}+|\p{White_Space}+$/gu;
const controls = /\p{Cc}/u;

// Go strings.TrimSpace uses Unicode White_Space; JS trim treats BOM differently.
export function trimDisplayName(value: string): string {
  return value.replace(whitespaceEdges, '');
}

function wellFormed(value: string): boolean {
  return !Array.from(value).some((char) => {
    const point = char.codePointAt(0) ?? 0;
    return point >= 0xd800 && point <= 0xdfff;
  });
}

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

const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

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

/** Email is the login identifier for all audiences; texts follow the prototypes. */
export function validateEmail(raw: string): string | null {
  const email = trimDisplayName(raw);
  if (!email) return 'Введите адрес почты, чтобы продолжить.';
  if (!wellFormed(raw) || !emailPattern.test(email))
    return 'Не похоже на настоящий адрес почты. Проверьте написание — например, name@example.com.';
  return null;
}

export interface PasswordCheck {
  id: 'length' | 'lower' | 'upper' | 'digit' | 'special';
  label: string;
  ok: boolean;
}

/** Requirements appear as a checklist once the user starts typing a password. */
export function passwordChecks(password: string): PasswordCheck[] {
  const length = Array.from(password).length;
  return [
    { id: 'length', label: 'От 8 до 64 символов', ok: length >= 8 && length <= 64 },
    { id: 'lower', label: 'Строчная буква (a-z)', ok: /[a-z]/.test(password) },
    { id: 'upper', label: 'Заглавная буква (A-Z)', ok: /[A-Z]/.test(password) },
    { id: 'digit', label: 'Цифра (0-9)', ok: /[0-9]/.test(password) },
    {
      id: 'special',
      label: 'Специальный символ (например, !%#)',
      ok: /[^A-Za-z0-9]/.test(password),
    },
  ];
}

/** Registration password error, validated on submit per the prototypes. */
export function validatePasswordStrength(password: string): string | null {
  if (!password) return 'Введите пароль, чтобы продолжить.';
  const length = Array.from(password).length;
  if (length < 8) return 'Пароль слишком короткий. Нужно не менее 8 символов.';
  if (length > 64) return 'Пароль слишком длинный. Не более 64 символов.';
  if (!wellFormed(password) || password.includes('\u0000') || utf8.encode(password).length > 72)
    return 'Пароль содержит недопустимые символы. Используйте буквы, цифры и спецсимволы.';
  if (passwordChecks(password).some((check) => !check.ok))
    return 'Пароль не соответствует требованиям надёжности — отметьте выполненные условия ниже.';
  return null;
}

export function validatePasswordRepeat(password: string, repeat: string): string | null {
  if (!repeat) return 'Повторите пароль, чтобы продолжить.';
  if (repeat !== password) return 'Пароли не совпадают. Введите пароль ещё раз в оба поля.';
  return null;
}

/** The emailed confirmation code is exactly six digits; non-digits are cut on input. */
export function normalizeCodeInput(raw: string): string {
  return raw.replace(/\D/g, '').slice(0, 6);
}

export function validateLoginCode(code: string): string | null {
  if (!code) return 'Введите код из письма.';
  if (code.length !== 6) return 'Код состоит из шести цифр.';
  return null;
}

/** Human-readable code lifetime for the login help text. */
export function formatCodeTtl(seconds: bigint): string {
  const total = Number(seconds);
  if (!Number.isFinite(total) || total < 60) return 'меньше минуты';
  const minutes = Math.round(total / 60);
  const remainder = minutes % 10;
  const teens = minutes % 100 >= 11 && minutes % 100 <= 14;
  const word =
    teens || remainder === 0 || remainder >= 5 ? 'минут' : remainder === 1 ? 'минута' : 'минуты';
  return `${minutes} ${word}`;
}
