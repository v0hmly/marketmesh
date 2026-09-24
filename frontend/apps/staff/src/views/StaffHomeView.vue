<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from "vue";
import { useRouter } from "vue-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { useStaffApi } from "../context";
import type { StaffSession } from "../api";
const api = useStaffApi();
const router = useRouter();
const session = ref<StaffSession | null>(null);
const pending = ref(true);
const error = ref(false);
let revision = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
let signingOut = false;
function clear() {
  revision++;
  session.value = null;
  clearTimeout(timer);
}
async function expire() {
  clear();
  await router.replace("/staff/login?expired=1");
}
async function load() {
  if (signingOut) return;
  clear();
  const attempt = revision;
  pending.value = true;
  error.value = false;
  try {
    const value = await api.getSession();
    if (attempt !== revision) return;
    session.value = value;
    timer = setTimeout(
      () => {
        void expire();
      },
      Math.max(0, value.expiresAt - Date.now()),
    );
  } catch (reason) {
    if (attempt !== revision) return;
    if (reason instanceof ConnectError && reason.code === Code.Unauthenticated)
      await expire();
    else error.value = true;
  } finally {
    if (attempt === revision) pending.value = false;
  }
}
async function logout() {
  signingOut = true;
  clear();
  pending.value = true;
  error.value = false;
  // A failed/uncertain logout never restores cached personal data.
  try {
    await api.logout();
    await router.replace("/staff/login");
  } catch {
    error.value = true;
  } finally {
    pending.value = false;
  }
}
function visibility() {
  if (document.visibilityState === "visible") void load();
}
onMounted(() => {
  void load();
  document.addEventListener("visibilitychange", visibility);
});
onBeforeUnmount(() => {
  clear();
  document.removeEventListener("visibilitychange", visibility);
});
</script>
<template>
  <section
    class="auth-layout"
    aria-labelledby="staff-home-title"
    :aria-busy="pending"
  >
    <h1 id="staff-home-title">Рабочий аккаунт.</h1>
    <p v-if="pending" role="status">Проверяем доступ…</p>
    <div v-else-if="error" class="card state-card">
      <p class="notice error" role="alert">
        Не удалось проверить состояние сессии. Войдите снова, чтобы продолжить.
      </p>
      <RouterLink class="button secondary" to="/staff/login"
        >Перейти ко входу</RouterLink
      >
    </div>
    <div v-else-if="session" class="card auth-card">
      <h2>{{ session.displayName || "Сотрудник" }}</h2>
      <p>{{ session.email }}</p>
      <p v-if="session.role">Доступ к порталу подтверждён.</p>
      <p v-else>Доступ ещё не выдан. Откройте приглашение администратора.</p>
      <button class="button text-button" @click="logout">
        Выйти из рабочего аккаунта
      </button>
    </div>
  </section>
</template>
