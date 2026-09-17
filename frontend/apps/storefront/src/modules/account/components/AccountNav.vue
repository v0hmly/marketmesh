<script setup lang="ts">
import { addressesEnabled, settingsEnabled } from '../../../shared/features';
</script>
<template>
  <nav
    v-if="addressesEnabled || settingsEnabled"
    class="account-nav"
    aria-label="Разделы личного кабинета"
  >
    <RouterLink to="/account" exact-active-class="account-link-active">О себе</RouterLink>
    <RouterLink
      v-if="addressesEnabled"
      to="/account/addresses"
      exact-active-class="account-link-active"
      >Адреса доставки</RouterLink
    >
    <RouterLink
      v-if="settingsEnabled"
      to="/account/settings"
      exact-active-class="account-link-active"
      >Оформление</RouterLink
    >
  </nav>
</template>
