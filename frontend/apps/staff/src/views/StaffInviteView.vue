<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Code, ConnectError } from "@connectrpc/connect";
import { useRoute } from "vue-router";
import { useStaffApi } from "../context";
import type { Invite } from "../api";

const staffApi = useStaffApi();
const route = useRoute();

const token = computed(() => {
  const raw =
    new URLSearchParams(route.hash.slice(1)).get("token") ?? route.query.token;
  return typeof raw === "string" ? raw.trim() : "";
});

type Phase = "missing" | "loading" | "ready" | "error" | "login";
const phase = ref<Phase>(token.value ? "loading" : "missing");
const invite = ref<Invite | null>(null);
const accepting = ref(false);
const accepted = ref(false);
const acceptError = ref(false);

let revision = 0;
async function load() {
  const attempt = ++revision;
  accepting.value = false;
  accepted.value = false;
  acceptError.value = false;
  invite.value = null;
  if (!token.value) {
    phase.value = "missing";
    return;
  }
  phase.value = "loading";
  invite.value = null;
  try {
    const value = await staffApi.getInvite(token.value);
    if (attempt !== revision) return;
    invite.value = value;
    phase.value = "ready";
  } catch (error) {
    if (attempt !== revision) return;
    phase.value =
      error instanceof ConnectError && error.code === Code.Unauthenticated
        ? "login"
        : "error";
  }
}

async function accept() {
  if (accepting.value || accepted.value) return;
  const attempt = revision;
  accepting.value = true;
  acceptError.value = false;
  try {
    await staffApi.acceptInvite(token.value);
    if (attempt !== revision) return;
    accepted.value = true;
  } catch (error) {
    if (attempt !== revision) return;
    if (error instanceof ConnectError && error.code === Code.Unauthenticated) {
      invite.value = null;
      phase.value = "login";
    } else acceptError.value = true;
  } finally {
    if (attempt === revision) accepting.value = false;
  }
}

async function signIn() {
  const inviteToken = token.value;
  const attempt = revision;
  if (accepting.value) return;
  accepting.value = true;
  acceptError.value = false;
  try {
    await staffApi.logout();
    if (attempt !== revision) return;
    const url = await staffApi.startSso(inviteToken);
    if (attempt !== revision) return;
    window.location.assign(url);
  } catch {
    if (attempt === revision) acceptError.value = true;
  } finally {
    if (attempt === revision) accepting.value = false;
  }
}

watch(
  token,
  () => {
    void load();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  revision++;
});
</script>

<template>
  <section class="auth-layout" aria-labelledby="staff-invite-title">
    <div v-if="phase === 'missing'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">Ссылка неполная.</h1>
      <p>
        Мы не нашли в ней приглашение. Скопируйте ссылку из письма целиком и
        откройте её снова.
      </p>
    </div>

    <div
      v-else-if="phase === 'loading'"
      class="card state-card"
      aria-busy="true"
    >
      <span class="loading-dot" aria-hidden="true"></span>
      <h1 id="staff-invite-title" class="state-title">
        Проверяем приглашение…
      </h1>
      <p role="status">Это займёт пару секунд.</p>
    </div>

    <div v-else-if="phase === 'login'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">
        Подтвердите рабочую почту.
      </h1>
      <p>
        Войдите через рабочий аккаунт, для которого выдано приглашение. После
        входа вы вернётесь к этой ссылке.
      </p>
      <p v-if="acceptError" class="notice error" role="alert">
        Не удалось начать вход. Попробуйте ещё раз.
      </p>
      <button class="button primary" :disabled="accepting" @click="signIn">
        Войти через рабочий аккаунт
      </button>
    </div>
    <div v-else-if="phase === 'error'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">
        Не удалось проверить приглашение.
      </h1>
      <p>Сервер не ответил. Проверьте соединение и попробуйте ещё раз.</p>
      <button type="button" class="button secondary" @click="load">
        Проверить снова
      </button>
    </div>

    <template v-else-if="accepted">
      <div class="card state-card">
        <h1 id="staff-invite-title" class="state-title">Доступ открыт.</h1>
        <p>Приглашение принято. Вы вошли через рабочий аккаунт.</p>
        <RouterLink class="button primary wide" to="/staff"
          >Открыть портал<span aria-hidden="true">↗</span></RouterLink
        >
      </div>
    </template>

    <template v-else-if="invite?.state === 'active'">
      <div class="auth-heading">
        <span class="eyebrow">ВНУТРЕННИЙ ПОРТАЛ</span>
        <h1 id="staff-invite-title">Вас пригласили в портал.</h1>
        <p class="lede">Вам открыли доступ с ролью {{ invite.role }}.</p>
      </div>

      <div class="card auth-card" :aria-busy="accepting">
        <p class="subtle">
          Приглашение отправил администратор {{ invite.inviterName }}.
        </p>
        <p v-if="acceptError" class="notice error" role="alert">
          Не удалось принять приглашение. Проверьте соединение и попробуйте ещё
          раз.
        </p>
        <button
          type="button"
          class="button primary wide"
          :disabled="accepting"
          @click="accept"
        >
          <span>{{
            accepting ? "Проверяем приглашение…" : "Принять приглашение"
          }}</span>
          <span v-if="!accepting" aria-hidden="true">↗</span>
        </button>
      </div>
    </template>

    <template v-else-if="invite?.state === 'expired'">
      <div class="card state-card">
        <h1 id="staff-invite-title" class="state-title">
          Срок приглашения истёк.
        </h1>
        <p>
          Ссылка из письма больше не действует. Попросите администратора
          отправить новое приглашение.
        </p>
      </div>
      <p class="auth-alternative">
        Доступ уже настроен?
        <RouterLink to="/staff/login">Войдите через рабочий аккаунт</RouterLink>
      </p>
    </template>

    <div v-else-if="invite?.state === 'used'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">
        Приглашение уже использовано.
      </h1>
      <p>Доступ по этой ссылке уже открыт — просто войдите.</p>
      <RouterLink class="button primary wide" to="/staff/login"
        >Войти через рабочий аккаунт<span aria-hidden="true"
          >↗</span
        ></RouterLink
      >
    </div>

    <div v-else class="card state-card">
      <h1 id="staff-invite-title" class="state-title">
        Приглашение для другой почты.
      </h1>
      <p>
        Приглашение выдано для другого адреса. Выйдите из текущего аккаунта и
        откройте ссылку из письма снова.
      </p>
      <p v-if="acceptError" class="notice error" role="alert">
        Не удалось начать вход. Попробуйте ещё раз.
      </p>
      <button class="button secondary" :disabled="accepting" @click="signIn">
        Сменить рабочий аккаунт
      </button>
    </div>
  </section>
</template>
