import { describe, expect, it } from 'vitest';
import { trimDisplayName } from '../../shared/validation';
import { validateProfile } from './validation';

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
