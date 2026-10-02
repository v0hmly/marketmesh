<script setup lang="ts">
import '../style.css';
import { computed, ref, watch } from 'vue';
import { favoritesEnabled, ordersEnabled } from '../../../shared/features';
import { useSession } from '../../../shell/context';
import { useStorefront } from '../store';

const session = useSession();
const store = useStorefront();
const draft = ref(store.state.query);
const signedIn = computed(() =>
  ['authenticated', 'profilePending'].includes(session.state.value.status),
);
/** Имя кнопки для скринридера: «Корзина: 2 изделия», а не «Корзина 2». */
const cartLabel = computed(() => {
  const n = store.state.cart.length;
  if (!n) return 'Корзина пуста';
  const a = n % 10;
  const b = n % 100;
  const word =
    a === 1 && b !== 11
      ? 'изделие'
      : a >= 2 && a <= 4 && (b < 12 || b > 14)
        ? 'изделия'
        : 'изделий';
  return `Корзина: ${n} ${word}`;
});
watch(
  () => store.state.query,
  (query) => {
    draft.value = query;
  },
);
</script>

<template>
  <a class="button secondary storefront-catalog" href="#catalog">Каталог</a>
  <form class="storefront-search" role="search" @submit.prevent="store.search(draft)">
    <label for="storefront-query" class="visually-hidden">Поиск по изделиям и мастерским</label>
    <input id="storefront-query" v-model="draft" type="search" placeholder="Изделия и мастерские" />
    <button type="submit" class="button secondary">Найти</button>
  </form>
  <nav class="storefront-nav" aria-label="Покупки и аккаунт">
    <template v-if="signedIn"
      ><RouterLink v-if="favoritesEnabled" class="nav-link" to="/account/favorites"
        >Избранное</RouterLink
      ><RouterLink v-if="ordersEnabled" class="nav-link" to="/account/orders"
        >Заказы</RouterLink
      ></template
    >
    <button type="button" class="storefront-cart" :aria-label="cartLabel" @click="store.openCart()">
      Корзина
      <span
        class="storefront-count"
        :class="{ 'storefront-count-filled': store.state.cart.length }"
        >{{ store.state.cart.length }}</span
      >
    </button>
    <RouterLink v-if="signedIn" class="button secondary" to="/account">Личный кабинет</RouterLink>
    <RouterLink v-else class="button secondary" to="/login">Войти</RouterLink>
  </nav>
</template>
