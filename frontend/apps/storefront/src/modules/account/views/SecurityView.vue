<script setup lang="ts">
import '../style.css';
import { computed } from 'vue';
import { useSession } from '../../../shell/context';
import { AccountSecurity } from '../../auth/ui';
import ProfilePending from '../components/ProfilePending.vue';

/** Отдельный раздел безопасности — только в сборке без MarketMesh ID. */
const session = useSession();
const status = computed(() => session.state.value.status);
</script>

<template>
  <section class="account-section" aria-labelledby="security-page-title">
    <div class="account-heading">
      <h1 id="security-page-title">Вход и безопасность</h1>
      <p class="lede">
        Почта и пароль для входа, подтверждение кодом и устройства, с которых вы заходили.
      </p>
    </div>
    <ProfilePending
      v-if="status === 'profilePending'"
      title="Готовим ваш аккаунт"
      text="Вход выполнен. Настройки входа появятся после подготовки профиля."
    />
    <div v-else-if="status !== 'authenticated'" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(status)" role="status">Проверяем сессию…</p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы управлять входом и сеансами.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <AccountSecurity />
  </section>
</template>
