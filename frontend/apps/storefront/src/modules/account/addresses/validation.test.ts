import { describe, expect, it } from 'vitest';
import { emptyAddress, validateAddress } from './validation';
const valid = () => ({
  ...emptyAddress(),
  recipient: 'Анна',
  phone: '+7 (999) 123-45-67',
  country: 'Российская Федерация',
  city: 'Москва',
  streetHouse: 'Улица, дом 1',
});
describe('address validation', () => {
  it('supports freeform countries, Unicode limits and optional fields', () => {
    expect(
      validateAddress({
        ...valid(),
        recipient: '🪡'.repeat(120),
        country: '国'.repeat(80),
        comment: '\nТекст\t<em>не HTML</em>\n',
      }),
    ).toEqual({});
    expect(validateAddress({ ...valid(), recipient: '🪡'.repeat(121) })).toHaveProperty(
      'recipient',
    );
    expect(validateAddress({ ...valid(), country: ' '.repeat(320) + 'X' })).toHaveProperty(
      'country',
    );
  });
  it('rejects malformed UTF-16, controls, absent required fields and non-ASCII phones', () => {
    expect(validateAddress({ ...valid(), recipient: '\nАнна' })).toHaveProperty('recipient');
    expect(validateAddress({ ...valid(), city: '\ud800' })).toHaveProperty('city');
    expect(validateAddress({ ...valid(), streetHouse: 'улица\nдом' })).toHaveProperty(
      'streetHouse',
    );
    expect(validateAddress({ ...valid(), comment: 'text\u0000' })).toHaveProperty('comment');
    expect(validateAddress({ ...valid(), recipient: ' \u2003 ' })).toHaveProperty('recipient');
    for (const phone of [
      '123456',
      '1'.repeat(16),
      '+７ 1234567',
      '1234567x',
      '\u00a01234567',
      ' '.repeat(30) + '1234567',
    ])
      expect(validateAddress({ ...valid(), phone })).toHaveProperty('phone');
  });
});
