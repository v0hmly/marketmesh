import { describe, expect, it } from 'vitest';
import { trimDisplayName, validateCredentials, validateProfile } from './validation';

describe('existing User field boundaries', () => {
  it('counts Unicode codepoints and original UTF-8 bytes, including trimmed input', () => {
    expect(validateProfile('😀'.repeat(80), '😀'.repeat(1000))).toEqual({});
    expect(validateProfile('😀'.repeat(81), '').displayName).toBeTruthy();
    expect(validateProfile(' '.repeat(320) + 'a', '').displayName).toBeTruthy();
    expect(validateProfile('', '😀'.repeat(1001)).bio).toBeTruthy();
    expect(validateProfile('', '')).toEqual({});
  });
  it('matches Go White_Space trimming instead of JS BOM trimming', () => {
    expect(trimDisplayName('\u0085 Имя \u0085')).toBe('Имя');
    expect(trimDisplayName('\uFEFFИмя\uFEFF')).toBe('\uFEFFИмя\uFEFF');
    expect(validateProfile('\u0085Имя', '')).toEqual({});
  });
  it('rejects malformed surrogate pairs and forbidden controls while allowing plain text', () => {
    expect(validateProfile('\ud800', '').displayName).toBeTruthy();
    expect(validateProfile('', '\udfff').bio).toBeTruthy();
    expect(validateProfile('a\u0000b', '').displayName).toBeTruthy();
    expect(validateProfile('', 'a\r\nb').bio).toBeTruthy();
    expect(validateProfile('<b>A</b>', 'one\n\ttwo')).toEqual({});
  });
  it('checks password bytes without trimming or retaining secrets', () => {
    expect(validateCredentials('alice', 'short').password).toBeTruthy();
    expect(validateCredentials('алиса', '😀😀😀')).toEqual({});
    expect(validateCredentials('a b', 'long password').identifier).toBeTruthy();
    expect(validateCredentials(' alice ', 'long password')).toEqual({});
  });
});
