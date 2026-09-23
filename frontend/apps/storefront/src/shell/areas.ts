/**
 * Продуктовые области storefront по ADR-0010: покупатель, продавец, сотрудник.
 * Каждая область — модуль в src/modules с собственными маршрутами; shell
 * владеет композицией маршрутов, сессией, темой и общими состояниями.
 */
export type Area = 'buyer' | 'seller' | 'staff';

/** Страница входа области. */
export const areaLoginPath: Record<Area, string> = {
  buyer: '/login',
  seller: '/seller/login',
  staff: '/staff/login',
};

/** Домашняя страница области. */
export const areaHomePath: Record<Area, string> = {
  buyer: '/account',
  seller: '/seller',
  staff: '/staff',
};

/** Название области в подвале, если маршрут не задаёт своё (`meta.section`). */
export const areaTitle: Record<Area, string> = {
  buyer: 'Личный кабинет',
  seller: 'Портал продавца',
  staff: 'Для сотрудников',
};

declare module 'vue-router' {
  interface RouteMeta {
    /** Продуктовая область маршрута; обязательна для модульных маршрутов. */
    area?: Area;
    /** Название места для подвала, если оно точнее названия области. */
    section?: string;
  }
}
