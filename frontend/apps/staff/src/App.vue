<script setup lang="ts">
import { inject } from "vue";
import { invalidatedKey } from "./context";
const invalidated = inject(invalidatedKey, null);
import { useRouteRecovery } from "./recovery";
const recovery = useRouteRecovery();
import BrandMark from "@marketmesh/design-system/BrandMark.vue";
</script>
<template>
  <a class="skip-link" href="#main-content">Перейти к содержимому</a>
  <div class="site-wrap">
    <header class="site-header">
      <RouterLink
        class="brand"
        to="/staff/login"
        aria-label="MarketMesh — портал сотрудников"
        ><BrandMark /><span
          >MarketMesh<span class="brand-dot">.</span></span
        ></RouterLink
      >
      <span class="brand-caption">Внутренний портал</span>
    </header>
    <main id="main-content" tabindex="-1">
      <section
        v-if="recovery?.failedPath.value"
        class="card state-card"
        role="alert"
      >
        <h1 class="state-title">Не удалось открыть раздел.</h1>
        <p>
          Возможно, сайт обновился или пропало соединение. Обновите страницу,
          чтобы продолжить. Несохранённые изменения будут потеряны.
        </p>
        <button class="button secondary" @click="recovery.reload()">
          Обновить страницу
        </button>
      </section>
      <RouterView v-else-if="!invalidated" />
      <section v-else class="card state-card" role="status">
        <h1 class="state-title">Сессия завершена.</h1>
        <p>Открываем страницу входа…</p>
      </section>
    </main>
    <footer class="site-footer">
      <span>MarketMesh</span><span>Для сотрудников</span>
    </footer>
  </div>
</template>
