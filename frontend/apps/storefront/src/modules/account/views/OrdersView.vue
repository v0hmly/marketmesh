<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import { useAccountCounts } from '../counts';
import { loadSampleOrders, type SampleOrder, type SampleOrderStatus } from '../sample-data';

const session = useSession();
const counts = useAccountCounts();
const orders = ref<SampleOrder[] | null>(null);
const loading = ref(false);
const failure = ref('');
const feedback = ref('');
const filter = ref<SampleOrderStatus | 'all'>('all');
const revealed = ref<string[]>([]);
let revision = 0;
let active = true;
const permitted = computed(() => session.state.value.status === 'authenticated');

const filters: { value: SampleOrderStatus | 'all'; label: string }[] = [
  { value: 'all', label: 'Все' },
  { value: 'shipping', label: 'В пути' },
  { value: 'ready', label: 'Готовы к получению' },
  { value: 'done', label: 'Получены' },
  { value: 'cancelled', label: 'Отменённые' },
];
const emptyText: Record<SampleOrderStatus | 'all', string> = {
  all: 'Заказов пока нет.',
  shipping: 'В пути ничего нет.',
  ready: 'Нет заказов, готовых к получению.',
  done: 'Полученных заказов пока нет.',
  cancelled: 'Отменённых заказов нет.',
};
const statusLabel: Record<SampleOrderStatus, string> = {
  shipping: 'В пути',
  ready: 'Готов к получению',
  done: 'Получен',
  cancelled: 'Отменён',
};
const stepNames = ['Собран', 'Передан в доставку', 'В пути', 'Готов к получению'];
const primaryLabel: Partial<Record<SampleOrderStatus, string>> = {
  shipping: 'Отследить',
  ready: 'Показать код',
  done: 'Оставить отзыв',
};
const visible = computed(
  () =>
    orders.value?.filter((order) => filter.value === 'all' || order.status === filter.value) ?? [],
);
const isActive = (order: SampleOrder) => order.status === 'shipping' || order.status === 'ready';

function clear() {
  revision++;
  orders.value = null;
  failure.value = '';
  feedback.value = '';
  filter.value = 'all';
  revealed.value = [];
  loading.value = false;
}
async function read() {
  if (loading.value || !permitted.value || !active) return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const value = await loadSampleOrders();
    if (attempt !== revision || !active) return;
    orders.value = value;
    if (counts) counts.orders = value.length;
  } catch {
    if (attempt !== revision) return;
    failure.value =
      'Не удалось загрузить заказы. Мы ничего не меняли в ваших данных. Повторите загрузку.';
  } finally {
    if (attempt === revision) loading.value = false;
  }
}
function primary(order: SampleOrder) {
  if (order.status === 'ready') {
    if (!revealed.value.includes(order.id)) revealed.value = [...revealed.value, order.id];
    feedback.value = 'Покажите код сотруднику пункта выдачи.';
  } else if (order.status === 'shipping') {
    feedback.value = 'Заказ в пути: посылка прошла сортировочный центр.';
  } else if (order.status === 'done') {
    feedback.value = 'Открыли форму отзыва в разделе «Отзывы».';
  }
}
function repeat(order: SampleOrder) {
  feedback.value = `Товары из заказа ${order.number} добавлены в корзину.`;
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
    if (permitted.value && !orders.value) void read();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  clear();
});
</script>

<template>
  <section class="account-section" aria-labelledby="orders-title">
    <div class="account-heading">
      <h1 id="orders-title">Заказы</h1>
      <p class="lede">Всё, что вы заказали: состав, получение и следующий шаг по каждому заказу.</p>
    </div>
    <div v-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы увидеть свои заказы.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!orders" class="card state-card" :aria-busy="loading">
      <template v-if="loading"
        ><span class="loading-dot" aria-hidden="true"></span>
        <p role="status">Загружаем заказы…</p></template
      >
      <template v-else
        ><h2>Заказы пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="sample-content">
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div class="filter-row" role="group" aria-label="Фильтр заказов">
        <button
          v-for="item in filters"
          :key="item.value"
          type="button"
          class="button secondary"
          :aria-pressed="filter === item.value"
          @click="filter = item.value"
        >
          {{ item.label }}
        </button>
      </div>
      <p v-if="!visible.length" class="card sample-empty">{{ emptyText[filter] }}</p>
      <ul v-else class="sample-list" aria-label="Список заказов">
        <li v-for="order in visible" :key="order.id" class="card sample-card">
          <div class="card-heading order-heading">
            <div class="order-title">
              <h2>Заказ {{ order.number }}</h2>
              <span class="draft-badge">{{ statusLabel[order.status] }}</span>
            </div>
            <div class="order-summary">
              <span class="order-placed">{{ order.placed }}</span>
              <span class="order-total">{{ order.total }}</span>
            </div>
          </div>
          <ol v-if="isActive(order)" class="order-steps" aria-label="Этапы доставки">
            <li
              v-for="(name, index) in stepNames"
              :key="name"
              :class="{ 'step-done': index <= (order.step ?? 0) }"
              :aria-current="index === (order.step ?? 0) ? 'step' : undefined"
            >
              {{ name }}
            </li>
          </ol>
          <ul class="order-lines" aria-label="Состав заказа">
            <li v-for="line in order.items" :key="line.title">
              <span class="sample-photo photo-line" aria-hidden="true">ФОТО</span>
              <span class="line-meta"
                ><span>{{ line.title }}</span
                ><span class="subtle">{{ line.meta }}</span></span
              >
              <span class="line-amount">{{ line.amount }}</span>
            </li>
          </ul>
          <div class="card-footer">
            <span class="subtle order-delivery">{{ order.delivery }}</span>
            <div class="button-row">
              <button
                v-if="primaryLabel[order.status]"
                class="button secondary"
                @click="primary(order)"
              >
                {{ primaryLabel[order.status]
                }}<span class="visually-hidden">: заказ {{ order.number }}</span></button
              ><button class="button text-button" @click="repeat(order)">
                Повторить заказ<span class="visually-hidden"> {{ order.number }}</span>
              </button>
            </div>
          </div>
          <div v-if="order.status === 'ready' && revealed.includes(order.id)" class="code-panel">
            <span>Код получения в пункте выдачи</span>
            <span class="code-value">{{ order.code }}</span>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>
