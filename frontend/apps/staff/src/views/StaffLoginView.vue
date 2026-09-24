<script setup lang="ts">
import { ref, onBeforeUnmount } from "vue";
import { useRoute } from "vue-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { useStaffApi } from "../context";

const staffApi = useStaffApi();
const route = useRoute();

let disposed = false;
onBeforeUnmount(() => {
  disposed = true;
});
const submitting = ref(false);
const outsideNetwork = ref(false);
const serverError = ref(route.query.failed === "1");
const sessionExpired = ref(route.query.expired === "1");

async function startSso() {
  if (submitting.value || outsideNetwork.value) return;
  submitting.value = true;
  serverError.value = false;
  sessionExpired.value = false;
  try {
    const url = await staffApi.startSso();
    if (disposed) return;
    window.location.assign(url);
  } catch (error) {
    if (disposed) return;
    outsideNetwork.value =
      error instanceof ConnectError && error.code === Code.PermissionDenied;
    serverError.value = !outsideNetwork.value;
  } finally {
    if (!disposed) submitting.value = false;
  }
}
</script>

<template>
  <section class="auth-layout" aria-labelledby="staff-auth-title">
    <div v-if="outsideNetwork" class="card state-card">
      <h1 id="staff-auth-title" class="state-title">
        Портал открыт только из корпоративной сети.
      </h1>
      <p>
        Запрос пришёл извне, и мы его не пропустили. Подключитесь к VPN и
        обновите страницу.
      </p>
      <button
        type="button"
        class="button secondary"
        @click="outsideNetwork = false"
      >
        Проверить снова
      </button>
    </div>

    <template v-else>
      <div class="auth-heading">
        <span class="eyebrow">ВНУТРЕННИЙ ПОРТАЛ</span>
        <h1 id="staff-auth-title">Войдите через рабочий аккаунт.</h1>
        <p class="lede">
          Доступ выдаёт администратор. Пароля у портала нет — вход подтверждает
          корпоративная учётная запись.
        </p>
      </div>

      <div class="card auth-card" :aria-busy="submitting">
        <p v-if="sessionExpired" class="notice error" role="alert">
          Сессия завершена. Войдите снова, чтобы продолжить работу.
        </p>
        <p v-if="serverError" class="notice error" role="alert">
          Не удалось подключиться к серверу. Проверьте соединение и попробуйте
          ещё раз.
        </p>
        <button
          type="button"
          class="button primary wide"
          :disabled="submitting"
          @click="startSso"
        >
          <span>{{
            submitting ? "Перенаправляем…" : "Войти через рабочий аккаунт"
          }}</span>
          <span v-if="!submitting" aria-hidden="true">↗</span>
        </button>
      </div>

      <p class="auth-alternative">
        Пришло приглашение в портал?
        <RouterLink to="/staff/invite">Откройте его</RouterLink>
      </p>
    </template>
  </section>
</template>
