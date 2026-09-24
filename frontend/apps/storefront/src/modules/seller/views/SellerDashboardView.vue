<script setup lang="ts">
import '../style.css';
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import SellerNav from '../components/SellerNav.vue';
import { loadSellerOverview, type SellerTask } from '../sample-data';

const session = useSession();
const shopName = ref('');
const shopStatus = ref('');
const shopPage = ref('');
const publishedCount = ref(0);
const nextPayout = ref('');
const tasks = ref<SellerTask[] | null>(null);
const lowStock = ref<{ id: string; title: string; left: string }[]>([]);
const loading = ref(false);
const failure = ref('');
const feedback = ref('');
let revision = 0;
let active = true;
const permitted = computed(() => session.state.value.status === 'authenticated');
const loaded = computed(() => tasks.value !== null);

function clear() {
  revision++;
  tasks.value = null;
  lowStock.value = [];
  failure.value = '';
  feedback.value = '';
  loading.value = false;
}
async function read() {
  if (loading.value || !permitted.value || !active) return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const overview = await loadSellerOverview();
    if (attempt !== revision || !active) return;
    shopName.value = overview.shop.name;
    shopStatus.value = overview.shop.statusLabel;
    shopPage.value = overview.shop.page;
    publishedCount.value = overview.shop.publishedCount;
    nextPayout.value = overview.shop.nextPayout;
    tasks.value = overview.tasks;
    lowStock.value = overview.lowStock;
  } catch {
    if (attempt !== revision) return;
    failure.value =
      'Не удалось загрузить обзор. Мы ничего не меняли в ваших данных. Повторите загрузку.';
  } finally {
    if (attempt === revision) loading.value = false;
  }
}
function act(task: SellerTask) {
  tasks.value = (tasks.value ?? []).filter((item) => item.id !== task.id);
  feedback.value = task.message;
}
watch(() => session.state.value.generation, clear, { flush: 'sync' });
watch(
  () => session.state.value.status,
  (status) => {
    if (['anonymous', 'switching', 'signingOut', 'uncertain', 'unsupported'].includes(status))
      clear();
  },
  { flush: 'sync' },
);
watch(
  () => [session.state.value.generation, session.state.value.subjectId, permitted.value] as const,
  () => {
    if (permitted.value && !tasks.value) void read();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  clear();
});
</script>

<template>
  <section aria-labelledby="seller-overview-title">
    <SellerNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ПОРТАЛ ПРОДАВЦА</span>
        <h1 id="seller-overview-title">Обзор</h1>
        <p class="lede">
          {{ loaded ? `Магазин «${shopName}». ` : '' }}Здесь собрано то, что ждёт вашего решения.
        </p>
      </div>
      <span class="section-number" aria-hidden="true">01 / ОБЗОР</span>
    </div>
    <div v-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Портал продавца начинается со входа</h2>
        <p>Войдите, чтобы увидеть свой магазин.</p>
        <RouterLink class="button primary" to="/seller/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!loaded" class="card state-card" :aria-busy="loading">
      <template v-if="loading"
        ><span class="loading-dot" aria-hidden="true"></span>
        <p role="status">Загружаем обзор…</p></template
      >
      <template v-else
        ><h2>Обзор пока недоступен</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="sample-content">
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div class="seller-grid">
        <div class="card seller-panel">
          <div class="card-heading">
            <h2>Требуют действия</h2>
            <RouterLink to="/seller/orders">Все заказы</RouterLink>
          </div>
          <ul v-if="tasks && tasks.length" class="seller-tasks" aria-label="Задачи магазина">
            <li v-for="task in tasks" :key="task.id" class="seller-task">
              <div class="seller-task-meta">
                <span class="seller-task-title">{{ task.title }}</span>
                <span class="subtle">{{ task.meta }}</span>
              </div>
              <button type="button" class="button secondary" @click="act(task)">
                {{ task.action }}<span class="visually-hidden">: {{ task.title }}</span>
              </button>
            </li>
          </ul>
          <p v-else class="subtle">Ничего не ждёт: все заказы обработаны, остатки в порядке.</p>
        </div>
        <div class="seller-column">
          <div class="card seller-panel">
            <h2>Магазин</h2>
            <dl class="seller-rows">
              <div class="seller-row">
                <dt>Состояние</dt>
                <dd>
                  <span class="draft-badge">{{ shopStatus }}</span>
                </dd>
              </div>
              <div class="seller-row">
                <dt>Страница</dt>
                <dd>{{ shopPage }}</dd>
              </div>
              <div class="seller-row">
                <dt>Изделий в продаже</dt>
                <dd>{{ publishedCount }}</dd>
              </div>
            </dl>
            <RouterLink class="button secondary seller-panel-action" to="/seller/products"
              >Открыть изделия</RouterLink
            >
          </div>
          <div class="card seller-panel">
            <h2>Остатки на исходе</h2>
            <ul v-if="lowStock.length" class="seller-rows" aria-label="Изделия с малым остатком">
              <li v-for="item in lowStock" :key="item.id" class="seller-row">
                <span>{{ item.title }}</span>
                <span class="subtle seller-count">{{ item.left }}</span>
              </li>
            </ul>
            <p v-else class="subtle">Остатки в порядке.</p>
            <p class="field-help">
              Пополнить партию можно в карточке изделия — покупателям видно только доступное
              количество.
            </p>
          </div>
          <div class="card seller-panel">
            <h2>Выплаты</h2>
            <p class="subtle">
              Ближайшая выплата — {{ nextPayout }}. Состав и расчёт появятся здесь после MM-56.
            </p>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
