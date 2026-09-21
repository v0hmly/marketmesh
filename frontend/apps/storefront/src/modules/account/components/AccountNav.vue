<script setup lang="ts">
import {
  addressesEnabled,
  favoritesEnabled,
  idEnabled,
  ordersEnabled,
  reviewsEnabled,
  settingsEnabled,
} from '../../../shared/features';

const sectionsEnabled =
  ordersEnabled ||
  favoritesEnabled ||
  reviewsEnabled ||
  addressesEnabled ||
  idEnabled ||
  settingsEnabled;
</script>
<template>
  <nav v-if="sectionsEnabled" class="account-nav" aria-label="Разделы личного кабинета">
    <RouterLink to="/account" exact-active-class="account-link-active">О себе</RouterLink>
    <RouterLink v-if="ordersEnabled" to="/account/orders" exact-active-class="account-link-active"
      >Заказы</RouterLink
    >
    <RouterLink
      v-if="favoritesEnabled"
      to="/account/favorites"
      exact-active-class="account-link-active"
      >Избранное</RouterLink
    >
    <RouterLink v-if="reviewsEnabled" to="/account/reviews" exact-active-class="account-link-active"
      >Отзывы</RouterLink
    >
    <RouterLink
      v-if="addressesEnabled"
      to="/account/addresses"
      exact-active-class="account-link-active"
      >Адреса доставки</RouterLink
    >
    <RouterLink v-if="idEnabled" to="/account/id" exact-active-class="account-link-active"
      >MarketMesh ID</RouterLink
    >
    <RouterLink
      v-if="settingsEnabled"
      to="/account/settings"
      exact-active-class="account-link-active"
      >Оформление</RouterLink
    >
  </nav>
</template>
