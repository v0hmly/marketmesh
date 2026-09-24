import { describe, expect, it } from 'vitest';
import { trimDisplayName } from './validation';
import {
  formatCodeTtl,
  normalizeCodeInput,
  passwordChecks,
  validateEmail,
  validateLoginCode,
  validatePasswordRepeat,
  validatePasswordStrength,
} from '../modules/auth/validation';

describe('email as the login identifier', () => {
  it('accepts trimmed plain addresses and rejects everything else', () => {
    expect(validateEmail('  anna@example.ru ')).toBeNull();
    expect(validateEmail('')).toBeTruthy();
    expect(validateEmail('   ')).toBeTruthy();
    expect(validateEmail('anna')).toBeTruthy();
    expect(validateEmail('анна@example.ru')).toBeTruthy();
    expect(validateEmail('Anna <anna@example.ru>')).toBeTruthy();
    expect(validateEmail('a'.repeat(250) + '@example.ru')).toBeTruthy();
    expect(validateEmail('anna@')).toBeTruthy();
    expect(validateEmail('anna@example')).toBeTruthy();
    expect(validateEmail('an na@example.ru')).toBeTruthy();
    expect(validateEmail('anna@ex ample.ru')).toBeTruthy();
    expect(validateEmail(String.fromCharCode(0xd800) + '@example.ru')).toBeTruthy();
  });
});

describe('display name trimming', () => {
  it('matches Go White_Space trimming instead of JS BOM trimming', () => {
    const bom = String.fromCharCode(0xfeff);
    expect(trimDisplayName(' Имя ')).toBe('Имя');
    expect(trimDisplayName(bom + 'Имя' + bom)).toBe(bom + 'Имя' + bom);
  });
});

describe('password strength checklist', () => {
  it('lists all five requirements with their state', () => {
    expect(passwordChecks('')).toEqual([
      { id: 'length', label: 'От 8 до 64 символов', ok: false },
      { id: 'lower', label: 'Строчная буква (a-z)', ok: false },
      { id: 'upper', label: 'Заглавная буква (A-Z)', ok: false },
      { id: 'digit', label: 'Цифра (0-9)', ok: false },
      { id: 'special', label: 'Специальный символ (например, !%#)', ok: false },
    ]);
    expect(passwordChecks('Aa1!bcde').every((check) => check.ok)).toBe(true);
  });
  it('validates on submit with prototype wording', () => {
    expect(validatePasswordStrength('')).toBe('Введите пароль, чтобы продолжить.');
    expect(validatePasswordStrength('Aa1!bcd')).toContain('не менее 8');
    expect(validatePasswordStrength('Aa1!' + 'b'.repeat(61))).toContain('Не более 64');
    expect(validatePasswordStrength('abcdefgh')).toContain('требованиям надёжности');
    expect(validatePasswordStrength('Aa1!bcde')).toBeNull();
    expect(validatePasswordStrength('Aa1 bcde')).toBeTruthy();
    expect(validatePasswordStrength('Aa1Жbcde')).toBeTruthy();
    expect(validatePasswordStrength('Aa1😀bcde')).toBeNull();
  });
  it('rejects malformed Unicode and oversized UTF-8 secrets', () => {
    expect(validatePasswordStrength('Aa1!bcd' + String.fromCharCode(0xd800))).toBeTruthy();
    expect(validatePasswordStrength('Aa1!' + String.fromCharCode(0) + 'bcd')).toBeTruthy();
    expect(validatePasswordStrength('Aa1!' + '😀'.repeat(18))).toBeTruthy();
  });
});

describe('password repeat and login code', () => {
  it('compares the repeat exactly', () => {
    expect(validatePasswordRepeat('Aa1!bcde', '')).toBeTruthy();
    expect(validatePasswordRepeat('Aa1!bcde', 'Aa1!bcdE')).toBeTruthy();
    expect(validatePasswordRepeat('Aa1!bcde', 'Aa1!bcde')).toBeNull();
  });
  it('keeps only six digits in the code input', () => {
    expect(normalizeCodeInput('48a29 13')).toBe('482913');
    expect(normalizeCodeInput('123456789')).toBe('123456');
    expect(normalizeCodeInput('')).toBe('');
  });
  it('validates the six-digit code', () => {
    expect(validateLoginCode('')).toBeTruthy();
    expect(validateLoginCode('48291')).toBeTruthy();
    expect(validateLoginCode('482913')).toBeNull();
  });
});

describe('code lifetime wording', () => {
  it('formats minutes with Russian pluralization', () => {
    expect(formatCodeTtl(30n)).toBe('меньше минуты');
    expect(formatCodeTtl(60n)).toBe('1 минута');
    expect(formatCodeTtl(120n)).toBe('2 минуты');
    expect(formatCodeTtl(300n)).toBe('5 минут');
    expect(formatCodeTtl(660n)).toBe('11 минут');
    expect(formatCodeTtl(1260n)).toBe('21 минута');
  });
});
