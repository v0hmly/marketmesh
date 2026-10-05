import { describe, expect, it } from 'vitest';
import { batchDateText, keepUnits, lowStock, quoted, shopName, stockText } from './store';

describe('бирка партии', () => {
  it('называет остаток словами для каждого состояния', () => {
    expect(stockText(0, 8)).toBe('Партия распродана');
    expect(stockText(2, 6)).toBe('Осталось 2 из 6');
    expect(stockText(2, 2)).toBe('Осталось 2 из 2');
    expect(stockText(7, 7)).toBe('В партии 7 из 7');
    expect(stockText(8, 10)).toBe('В партии 8 из 10');
  });

  it('отмечает охрой только заканчивающуюся партию', () => {
    expect(lowStock(2, 6)).toBe(true);
    expect(lowStock(1, 3)).toBe(true);
    expect(lowStock(2, 3)).toBe(false);
    expect(lowStock(3, 12)).toBe(false);
    expect(lowStock(0, 8)).toBe(false);
  });

  it('даёт дату партии отдельной строкой', () => {
    expect(batchDateText('2026-09-24')).toBe('партия от 24 сентября');
  });

  it('показывает мастерскую по имени и не отрывает единицы от чисел', () => {
    expect(shopName('Мастерская «Глина и соль»')).toBe('Глина и соль');
    expect(keepUnits('Кружка «Пена», 300 мл')).toBe('Кружка «Пена», 300\u00a0мл');
    expect(keepUnits('Льняная скатерть, 140 × 220')).toBe('Льняная скатерть, 140\u00a0×\u00a0220');
    expect(keepUnits('Набор открыток «Север», 6 шт.')).toBe('Набор открыток «Север», 6\u00a0шт.');
    expect(keepUnits('Салфетки, 4 шт')).toBe('Салфетки, 4\u00a0шт');
    expect(keepUnits('Миска 2 глиняные')).toBe('Миска 2 глиняные');
  });
});

describe('сообщения витрины', () => {
  it('меняет вложенные кавычки на „лапки“', () => {
    expect(quoted('Кружка «Пена», 300 мл')).toBe('«Кружка „Пена“, 300 мл»');
    expect(quoted('Подсвечник из ясеня')).toBe('«Подсвечник из ясеня»');
  });
});

describe('корзина витрины', () => {
  it('опустевшая корзина снимает ответ об оформлении', async () => {
    const { useStorefront } = await import('./store');
    const store = useStorefront();
    store.addToCart('p1');
    store.state.cartNotice = 'Оформление заказа пока недоступно.';
    store.removeFromCart('p1');
    expect(store.state.cartNotice).toBe('');
  });

  it('повтор ответа в корзине объявляется заново', async () => {
    const { nextTick } = await import('vue');
    const { useStorefront } = await import('./store');
    const store = useStorefront();
    store.setCartNotice('Оформление заказа пока недоступно.');
    await nextTick();
    store.setCartNotice('Оформление заказа пока недоступно.');
    expect(store.state.cartNotice).toBe('');
    await nextTick();
    expect(store.state.cartNotice).toBe('Оформление заказа пока недоступно.');
  });

  it('повтор действия объявляется в live-регионе заново', async () => {
    const { nextTick } = await import('vue');
    const { useStorefront } = await import('./store');
    const store = useStorefront();
    store.announce('«Кружка» в корзине.');
    await nextTick();
    store.announce('«Кружка» в корзине.');
    expect(store.state.notice).toBe('');
    await nextTick();
    expect(store.state.notice).toBe('«Кружка» в корзине.');
  });
});
