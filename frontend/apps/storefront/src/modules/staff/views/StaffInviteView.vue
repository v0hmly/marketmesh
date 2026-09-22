<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRoute } from 'vue-router';
import { useStaffApi } from '../../../shell/context';
import type { Invite } from '../../../shared/api/staff';

const staffApi = useStaffApi();
const route = useRoute();

const token = computed(() => {
  const raw = route.query.token;
  return typeof raw === 'string' ? raw.trim() : '';
});

type Phase = 'missing' | 'loading' | 'ready' | 'error';
const phase = ref<Phase>(token.value ? 'loading' : 'missing');
const invite = ref<Invite | null>(null);
const accepting = ref(false);
const accepted = ref(false);
const acceptError = ref(false);

async function load() {
  if (!token.value) {
    phase.value = 'missing';
    return;
  }
  phase.value = 'loading';
  invite.value = null;
  try {
    invite.value = await staffApi.getInvite(token.value);
    phase.value = 'ready';
  } catch {
    phase.value = 'error';
  }
}

async function accept() {
  if (accepting.value || accepted.value) return;
  accepting.value = true;
  acceptError.value = false;
  try {
    await staffApi.acceptInvite(token.value);
    accepted.value = true;
  } catch {
    acceptError.value = true;
  } finally {
    accepting.value = false;
  }
}

onMounted(() => {
  if (phase.value === 'loading') void load();
});
</script>

<template>
  <section class="auth-layout" aria-labelledby="staff-invite-title">
    <div v-if="phase === 'missing'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">Ссылка неполная.</h1>
      <p>Мы не нашли в ней приглашение. Скопируйте ссылку из письма целиком и откройте её снова.</p>
    </div>

    <div v-else-if="phase === 'loading'" class="card state-card" aria-busy="true">
      <span class="loading-dot" aria-hidden="true"></span>
      <h1 id="staff-invite-title" class="state-title">Проверяем приглашение…</h1>
      <p role="status">Это займёт пару секунд.</p>
    </div>

    <div v-else-if="phase === 'error'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">Не удалось проверить приглашение.</h1>
      <p>Сервер не ответил. Проверьте соединение и попробуйте ещё раз.</p>
      <button type="button" class="button secondary" @click="load">Проверить снова</button>
    </div>

    <template v-else-if="accepted">
      <div class="card state-card">
        <h1 id="staff-invite-title" class="state-title">Доступ открыт.</h1>
        <p>Приглашение принято. Теперь войдите через рабочий аккаунт.</p>
        <RouterLink class="button primary wide" to="/staff/login"
          >Войти через рабочий аккаунт<span aria-hidden="true">↗</span></RouterLink
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
        <p class="subtle">Приглашение отправил администратор {{ invite.inviterName }}.</p>
        <p v-if="acceptError" class="notice error" role="alert">
          Не удалось принять приглашение. Проверьте соединение и попробуйте ещё раз.
        </p>
        <button type="button" class="button primary wide" :disabled="accepting" @click="accept">
          <span>{{ accepting ? 'Проверяем приглашение…' : 'Принять приглашение' }}</span>
          <span v-if="!accepting" aria-hidden="true">↗</span>
        </button>
      </div>
    </template>

    <template v-else-if="invite?.state === 'expired'">
      <div class="card state-card">
        <h1 id="staff-invite-title" class="state-title">Срок приглашения истёк.</h1>
        <p>
          Ссылка из письма больше не действует. Попросите администратора отправить новое
          приглашение.
        </p>
      </div>
      <p class="auth-alternative">
        Доступ уже настроен?
        <RouterLink to="/staff/login">Войдите через рабочий аккаунт</RouterLink>
      </p>
    </template>

    <div v-else-if="invite?.state === 'used'" class="card state-card">
      <h1 id="staff-invite-title" class="state-title">Приглашение уже использовано.</h1>
      <p>Доступ по этой ссылке уже открыт — просто войдите.</p>
      <RouterLink class="button primary wide" to="/staff/login"
        >Войти через рабочий аккаунт<span aria-hidden="true">↗</span></RouterLink
      >
    </div>

    <div v-else class="card state-card">
      <h1 id="staff-invite-title" class="state-title">Приглашение для другой почты.</h1>
      <p>
        Приглашение выдано для другого адреса. Выйдите из текущего аккаунта и откройте ссылку из
        письма снова.
      </p>
    </div>
  </section>
</template>
