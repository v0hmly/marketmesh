const utf8 = new TextEncoder();
const whitespaceEdges = /^\p{White_Space}+|\p{White_Space}+$/gu;

// Go strings.TrimSpace uses Unicode White_Space; JS trim treats BOM differently.
export function trimDisplayName(value: string): string {
  return value.replace(whitespaceEdges, '');
}

export function wellFormed(value: string): boolean {
  return !Array.from(value).some((char) => {
    const point = char.codePointAt(0) ?? 0;
    return point >= 0xd800 && point <= 0xdfff;
  });
}

const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/** Email is the login identifier for all audiences; texts follow the prototypes. */
export function validateEmail(raw: string): string | null {
  const email = trimDisplayName(raw);
  if (!email) return 'Введите адрес почты, чтобы продолжить.';
  if (
    !wellFormed(raw) ||
    !emailPattern.test(email) ||
    email.length > 254 ||
    /[^\x21-\x7e]|[<>"\\(),;:]/u.test(email)
  )
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
      ok: /[^\p{L}\p{Nd}\p{White_Space}\p{Cc}]/u.test(password),
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
