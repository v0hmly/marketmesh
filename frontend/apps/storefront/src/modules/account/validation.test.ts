import { describe, expect, it, vi } from 'vitest';
import { trimDisplayName } from '../../shared/validation';
import { ageOf, birthLabel, validateIdentity, validateProfile } from './validation';

describe('existing User field boundaries', () => {
  it('counts Unicode codepoints and original UTF-8 bytes, including trimmed input', () => {
    expect(validateProfile('😀'.repeat(80), '😀'.repeat(1000))).toEqual({});
    expect(validateProfile('😀'.repeat(81), '').displayName).toBeTruthy();
    expect(validateProfile(' '.repeat(320) + 'a', '').displayName).toBeTruthy();
    expect(validateProfile('', '😀'.repeat(1001)).bio).toBeTruthy();
    expect(validateProfile('', '')).toEqual({});
  });
  it('matches Go White_Space trimming instead of JS BOM trimming', () => {
    expect(trimDisplayName(' Имя ')).toBe('Имя');
    expect(validateProfile('Имя', '')).toEqual({});
  });
  it('rejects malformed surrogate pairs and forbidden controls while allowing plain text', () => {
    expect(validateProfile(String.fromCharCode(0xd800), '').displayName).toBeTruthy();
    expect(validateProfile('', String.fromCharCode(0xdfff)).bio).toBeTruthy();
    expect(validateProfile('a' + String.fromCharCode(0) + 'b', '').displayName).toBeTruthy();
    expect(validateProfile('', 'a\r\nb').bio).toBeTruthy();
    expect(validateProfile('<b>A</b>', 'one\n\ttwo')).toEqual({});
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
    expect(validateProfile('\nВера', '').displayName).toBeTruthy();
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
