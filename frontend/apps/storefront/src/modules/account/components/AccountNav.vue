<script setup lang="ts">
import { computed } from 'vue';
import {
  addressesEnabled,
  favoritesEnabled,
  idEnabled,
  ordersEnabled,
  reviewsEnabled,
} from '../../../shared/features';
import type { AccountCounts, CountedSection } from '../counts';

const props = defineProps<{ counts?: AccountCounts }>();

interface NavItem {
  to: string;
  label: string;
  counted?: CountedSection;
  /** Начало фразы для чтения счётчика: «Заказы, всего 4». */
  countPrefix?: string;
}

const items: NavItem[] = [
  ...(ordersEnabled
    ? [{ to: '/account/orders', label: 'Заказы', counted: 'orders' as const, countPrefix: 'всего' }]
    : []),
  ...(favoritesEnabled
    ? [
        {
          to: '/account/favorites',
          label: 'Избранное',
          counted: 'favorites' as const,
          countPrefix: 'всего',
        },
      ]
    : []),
  ...(reviewsEnabled
    ? [
        {
          to: '/account/reviews',
          label: 'Отзывы',
          counted: 'reviews' as const,
          countPrefix: 'ждут отзыва:',
        },
      ]
    : []),
  ...(addressesEnabled ? [{ to: '/account/addresses', label: 'Адреса доставки' }] : []),
  // Без MarketMesh ID безопасность остаётся отдельным разделом (сборка до выкатки ID).
  idEnabled
    ? { to: '/account/id', label: 'MarketMesh ID' }
    : { to: '/account/security', label: 'Вход и безопасность' },
];

const shown = computed(() =>
  items.map((item) => {
    const count = item.counted ? props.counts?.[item.counted] : undefined;
    // Пробел в конце — часть текста для чтения: «Отзывы, ждут отзыва: 2».
    return { ...item, count: count && count > 0 ? count : null, hint: `, ${item.countPrefix} ` };
  }),
);
</script>

<template>
  <nav class="account-nav" aria-label="Разделы личного кабинета">
    <ul>
      <li v-for="item in shown" :key="item.to">
        <RouterLink :to="item.to" exact-active-class="account-link-active"
          ><span>{{ item.label }}</span
          ><span v-if="item.count !== null" class="account-nav-count"
            ><span class="visually-hidden">{{ item.hint }}</span
            >{{ item.count }}</span
          ></RouterLink
        >
      </li>
    </ul>
  </nav>
</template>
