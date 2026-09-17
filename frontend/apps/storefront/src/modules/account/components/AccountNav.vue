<script setup lang="ts">
import { addressesEnabled } from '../../../shared/features';
</script>
<template>
  <nav v-if="addressesEnabled" class="account-nav" aria-label="Разделы личного кабинета">
    <RouterLink to="/account" exact-active-class="account-link-active">О себе</RouterLink>
    <RouterLink to="/account/addresses" exact-active-class="account-link-active"
      >Адреса доставки</RouterLink
    >
  </nav>
</template>
