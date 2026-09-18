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

export function validateCredentials(
  identifier: string,
  password: string,
): { identifier?: string; password?: string } {
  const errors: { identifier?: string; password?: string } = {};
  const trimmed = trimDisplayName(identifier);
  // Auth owns Unicode case normalization and the resulting byte-length bound.
  if (!wellFormed(identifier) || !trimmed || /[\p{White_Space}\p{Cc}]/u.test(trimmed)) {
    errors.identifier = 'Укажите логин без пробелов и управляющих символов.';
  }
  const size = utf8.encode(password).length;
  const characters = Array.from(password).length;
  if (!wellFormed(password) || characters < 8 || characters > 64) {
    errors.password = 'Пароль должен содержать от 8 до 64 символов.';
  } else if (password.includes('\u0000')) {
    errors.password = 'Пароль содержит недопустимый символ.';
  } else if (size > 72) {
    errors.password =
      'Пароль слишком длинный: кириллица и эмодзи занимают больше места. Сократите его.';
  }
  return errors;
}
