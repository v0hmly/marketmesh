import { describe, expect, it } from 'vitest';
import { batchDateText, lowStock, stockText } from './store';

describe('бирка партии', () => {
  it('называет остаток словами для каждого состояния', () => {
    expect(stockText(0, 8)).toBe('Партия распродана');
    expect(stockText(2, 6)).toBe('Осталось 2 из 6');
    expect(stockText(2, 2)).toBe('Осталось 2 из 2');
    expect(stockText(7, 7)).toBe('Вся партия: 7');
    expect(stockText(8, 10)).toBe('В партии 8 из 10');
  });

  it('отмечает охрой только заканчивающуюся партию', () => {
    expect(lowStock(2, 6)).toBe(true);
    expect(lowStock(1, 3)).toBe(true);
    expect(lowStock(2, 3)).toBe(false);
    expect(lowStock(3, 12)).toBe(false);
    expect(lowStock(0, 8)).toBe(false);
  });

  it('не повторяет слово «партия» в дате', () => {
    expect(batchDateText('2026-09-24', 'Осталось 2 из 6')).toBe('партия от 24 сентября');
    expect(batchDateText('2026-09-24', 'В партии 8 из 10')).toBe('от 24 сентября');
    expect(batchDateText('2026-09-24', 'Партия распродана')).toBe('от 24 сентября');
  });
});
