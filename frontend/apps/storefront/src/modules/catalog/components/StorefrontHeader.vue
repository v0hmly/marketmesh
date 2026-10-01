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
    <input
      id="storefront-query"
      v-model="draft"
      type="search"
      placeholder="Изделия, мастерские, материалы"
    />
    <button type="submit" class="button secondary">Найти</button>
  </form>
  <nav class="storefront-nav" aria-label="Ваше">
    <template v-if="signedIn"
      ><RouterLink v-if="favoritesEnabled" class="nav-link" to="/account/favorites"
        >Избранное</RouterLink
      ><RouterLink v-if="ordersEnabled" class="nav-link" to="/account/orders"
        >Заказы</RouterLink
      ></template
    >
    <button type="button" class="storefront-cart" @click="store.openCart()">
      Корзина <span class="storefront-count">{{ store.state.cart.length }}</span>
    </button>
    <RouterLink v-if="signedIn" class="button secondary" to="/account">Личный кабинет</RouterLink>
    <RouterLink v-else class="button secondary" to="/login">Войти</RouterLink>
  </nav>
</template>
