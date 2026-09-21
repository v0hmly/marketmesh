<template>
  <nav class="account-nav" aria-label="Разделы портала продавца">
    <RouterLink to="/seller" exact-active-class="account-link-active">Обзор</RouterLink>
    <RouterLink to="/seller/products" exact-active-class="account-link-active">Изделия</RouterLink>
    <RouterLink to="/seller/orders" exact-active-class="account-link-active">Заказы</RouterLink>
  </nav>
</template>
