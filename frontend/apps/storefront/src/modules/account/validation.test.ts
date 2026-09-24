import { describe, expect, it, vi } from 'vitest';
import { trimDisplayName } from '../../shared/validation';
import { ageOf, birthLabel, publicLineOf, validateIdentity } from './validation';

describe('display name boundaries', () => {
  const draft = { displayName: 'Вера', lastName: '', birthDate: '', phone: '', city: '' };
  const nameError = (displayName: string) =>
    validateIdentity({ ...draft, displayName }).displayName;
  it('counts Unicode codepoints and original UTF-8 bytes, including trimmed input', () => {
    expect(nameError('😀'.repeat(80))).toBeUndefined();
    expect(nameError('😀'.repeat(81))).toBeTruthy();
    expect(nameError(' '.repeat(320) + 'a')).toBeTruthy();
  });
  it('matches Go White_Space trimming instead of JS BOM trimming', () => {
    expect(trimDisplayName(' Имя ')).toBe('Имя');
    expect(nameError(' Имя ')).toBeUndefined();
  });
  it('rejects malformed surrogate pairs and forbidden controls while allowing plain text', () => {
    expect(nameError(String.fromCharCode(0xd800))).toBeTruthy();
    expect(nameError('a' + String.fromCharCode(0) + 'b')).toBeTruthy();
    expect(nameError('<b>A</b>')).toBeUndefined();
  });
});

describe('public line next to a review', () => {
  it('joins name, city and the age only when it may be shown', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-23T12:00:00Z'));
    try {
      const identity = { displayName: ' Вера ', city: 'Санкт-Петербург', birthDate: '1989-03-12' };
      expect(publicLineOf({ ...identity, showAge: false })).toBe('Вера, Санкт-Петербург');
      expect(publicLineOf({ ...identity, showAge: true })).toBe('Вера, Санкт-Петербург, 37 лет');
      expect(publicLineOf({ ...identity, city: '', showAge: true })).toBe('Вера, 37 лет');
      expect(publicLineOf({ displayName: '', city: '', birthDate: '', showAge: true })).toBe(
        'Покупатель MarketMesh',
      );
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('private identity boundaries', () => {
  const draft = { displayName: 'Вера', lastName: '', birthDate: '', phone: '', city: '' };
  it('rejects impossible dates and uses the UTC day for future dates', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-22T23:30:00-12:00'));
    try {
      for (const birthDate of [
        '0000-01-01',
        '1900-02-29',
        '2026-02-30',
        '2026-13-01',
        '2026-09-24',
      ])
        expect(validateIdentity({ ...draft, birthDate }).birthDate).toBeTruthy();
      for (const birthDate of ['', '0001-01-01', '2000-02-29', '2026-09-23'])
        expect(validateIdentity({ ...draft, birthDate })).toEqual({});
      expect(ageOf('2026-02-30')).toBeNull();
      expect(birthLabel('2026-02-30')).toBe('Не указана');
      expect(ageOf('2000-09-23')).toBe(26);
    } finally {
      vi.useRealTimers();
    }
  });
  it('enforces raw byte, Unicode and control limits before trimming', () => {
    expect(
      validateIdentity({ ...draft, lastName: '😀'.repeat(80), city: '😀'.repeat(120) }),
    ).toEqual({});
    for (const lastName of [
      '😀'.repeat(81),
      ' '.repeat(321),
      '\tФамилия',
      String.fromCharCode(0xd800),
    ])
      expect(validateIdentity({ ...draft, lastName }).lastName).toBeTruthy();
    for (const city of ['界'.repeat(121), ' '.repeat(481), 'Город\u0085'])
      expect(validateIdentity({ ...draft, city }).city).toBeTruthy();
    expect(validateIdentity({ ...draft, displayName: '\nВера' }).displayName).toBeTruthy();
  });
  it('allows only bounded ASCII phone formatting with seven to fifteen digits', () => {
    for (const phone of ['', ' ', '+7 (999) 123-45-67', '1234567', '123456789012345'])
      expect(validateIdentity({ ...draft, phone })).toEqual({});
    for (const phone of [
      '123456',
      '1234567890123456',
      '1234567доб',
      '１２３４５６７',
      '1234567\n',
      ' '.repeat(26) + '1234567',
    ])
      expect(validateIdentity({ ...draft, phone }).phone).toBeTruthy();
  });
});
