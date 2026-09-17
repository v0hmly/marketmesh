import type { AddressInput } from '../../shared/api/types';
import { trimDisplayName } from './validation';

export const addressFields = [
  {
    key: 'recipient',
    label: 'Получатель',
    limit: 120,
    required: true,
    autocomplete: 'shipping name',
  },
  {
    key: 'phone',
    label: 'Телефон получателя',
    limit: 32,
    required: true,
    autocomplete: 'shipping tel',
  },
  {
    key: 'country',
    label: 'Страна',
    limit: 80,
    required: true,
    autocomplete: 'shipping country-name',
  },
  {
    key: 'postalCode',
    label: 'Почтовый индекс',
    limit: 20,
    required: false,
    autocomplete: 'shipping postal-code',
  },
  {
    key: 'city',
    label: 'Город / населённый пункт',
    limit: 120,
    required: true,
    autocomplete: 'shipping address-level2',
  },
  {
    key: 'streetHouse',
    label: 'Улица и дом',
    limit: 240,
    required: true,
    autocomplete: 'shipping address-line1',
  },
  {
    key: 'apartment',
    label: 'Квартира / помещение',
    limit: 40,
    required: false,
    autocomplete: 'shipping address-line2',
  },
  { key: 'comment', label: 'Комментарий', limit: 500, required: false, autocomplete: 'off' },
] as const;
export type AddressErrors = Partial<Record<keyof AddressInput, string>>;
export const emptyAddress = (): AddressInput => ({
  recipient: '',
  phone: '',
  country: '',
  postalCode: '',
  city: '',
  streetHouse: '',
  apartment: '',
  comment: '',
});
export function copyAddress(fields: AddressInput): AddressInput {
  const copy = emptyAddress();
  for (const { key } of addressFields) copy[key] = fields[key];
  return copy;
}
export function validateAddress(fields: AddressInput): AddressErrors {
  const errors: AddressErrors = {};
  const encoder = new TextEncoder();
  for (const field of addressFields) {
    const raw = fields[field.key];
    const value = trimDisplayName(raw);
    const wellFormed = !Array.from(raw).some((char) => {
      const point = char.codePointAt(0)!;
      return point >= 0xd800 && point <= 0xdfff;
    });
    const controlled = /\p{Cc}/u.test(field.key === 'comment' ? raw.replace(/[\n\t]/g, '') : raw);
    if (
      !wellFormed ||
      encoder.encode(raw).length > (field.key === 'phone' ? 32 : field.limit * 4) ||
      Array.from(value).length > field.limit ||
      controlled
    )
      errors[field.key] =
        `${field.label}: не более ${field.limit} символов; недопустимые символы и слишком длинный исходный текст запрещены.`;
    else if (field.required && !value) errors[field.key] = 'Заполните это поле.';
    else if (
      field.key === 'phone' &&
      (!/^[0-9 +.()\-]+$/.test(raw) ||
        value.replace(/\D/g, '').length < 7 ||
        value.replace(/\D/g, '').length > 15)
    )
      errors.phone =
        'Укажите от 7 до 15 цифр. Разрешены пробелы, +, − (обычный дефис), точка и скобки.';
  }
  return errors;
}
