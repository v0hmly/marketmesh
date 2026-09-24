<script setup lang="ts">
import '../style.css';
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import SellerNav from '../components/SellerNav.vue';
import { loadSellerOrders, type SellerOrder, type SellerOrderStatus } from '../sample-data';

const session = useSession();
const orders = ref<SellerOrder[] | null>(null);
const loading = ref(false);
const failure = ref('');
const feedback = ref('');
const filter = ref<'active' | 'done' | 'cancelled'>('active');
let revision = 0;
let active = true;
const permitted = computed(() => session.state.value.status === 'authenticated');

const filters: { value: 'active' | 'done' | 'cancelled'; label: string }[] = [
  { value: 'active', label: 'В работе' },
  { value: 'done', label: 'Завершённые' },
  { value: 'cancelled', label: 'Отменённые' },
];
const emptyText: Record<'active' | 'done' | 'cancelled', string> = {
  active: 'Заказов в работе нет.',
  done: 'Завершённых заказов пока нет.',
  cancelled: 'Отменённых заказов нет.',
};
const statusMeta: Record<
  SellerOrderStatus,
  { label: string; action: string; next: SellerOrderStatus | ''; hint: string; message: string }
> = {
  new: {
    label: 'Новый',
    action: 'Подтвердить заказ',
    next: 'packing',
    hint: 'Подтвердите заказ, чтобы начать сборку.',
    message: 'Заказ подтверждён. Соберите изделия и передайте их в доставку.',
  },
  packing: {
    label: 'Собирается',
    action: 'Передать в доставку',
    next: 'shipped',
    hint: 'Изделия зарезервированы за покупателем.',
    message: 'Заказ передан в доставку. Покупатель увидит трек-номер.',
  },
  shipped: {
    label: 'В доставке',
    action: '',
    next: '',
    hint: 'Ждём подтверждения получения от покупателя.',
    message: '',
  },
  done: {
    label: 'Завершён',
    action: '',
    next: '',
    hint: 'Изделия получены, заказ закрыт.',
    message: '',
  },
  cancelled: {
    label: 'Отменён',
    action: '',
    next: '',
    hint: 'Резерв освобождён, изделия вернулись в продажу.',
    message: '',
  },
};
function inFilter(order: SellerOrder): boolean {
  if (filter.value === 'active') return ['new', 'packing', 'shipped'].includes(order.status);
  if (filter.value === 'done') return order.status === 'done';
  return order.status === 'cancelled';
}
const visible = computed(() => orders.value?.filter(inFilter) ?? []);

function clear() {
  revision++;
  orders.value = null;
  failure.value = '';
  feedback.value = '';
  filter.value = 'active';
  loading.value = false;
}
async function read() {
  if (loading.value || !permitted.value || !active) return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const value = await loadSellerOrders();
    if (attempt !== revision || !active) return;
    orders.value = value;
  } catch {
    if (attempt !== revision) return;
    failure.value =
      'Не удалось загрузить заказы. Мы ничего не меняли в ваших данных. Повторите загрузку.';
  } finally {
    if (attempt === revision) loading.value = false;
  }
}
function advance(order: SellerOrder) {
  const meta = statusMeta[order.status];
  if (!meta.next) return;
  orders.value = (orders.value ?? []).map((candidate) =>
    candidate.id === order.id
      ? { ...candidate, status: meta.next as SellerOrderStatus }
      : candidate,
  );
  feedback.value = meta.message;
}
function open(order: SellerOrder) {
  feedback.value = `Заказ ${order.number} открыт. Состав и данные получения зафиксированы на момент покупки.`;
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
  <section aria-labelledby="seller-orders-title">
    <SellerNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ПОРТАЛ ПРОДАВЦА</span>
        <h1 id="seller-orders-title">Заказы</h1>
        <p class="lede">
          Состав, цена и данные получения сохранены на момент покупки и не меняются задним числом.
        </p>
      </div>
      <span class="section-number" aria-hidden="true">03 / ЗАКАЗЫ</span>
    </div>
    <div v-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Портал продавца начинается со входа</h2>
        <p>Войдите, чтобы увидеть заказы магазина.</p>
        <RouterLink class="button primary" to="/seller/login"
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
      <div class="filter-row" role="group" aria-label="Фильтр по состоянию">
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
              <span class="draft-badge">{{ statusMeta[order.status].label }}</span>
            </div>
            <span class="order-placed">{{ order.placed }}</span>
          </div>
          <div class="seller-order-body">
            <ul class="seller-order-lines" aria-label="Состав заказа">
              <li v-for="line in order.items" :key="line.title">
                <span>{{ line.title }}</span>
                <span class="line-amount subtle">{{ line.amount }}</span>
              </li>
            </ul>
            <dl class="seller-rows">
              <div class="seller-row">
                <dt>ПОЛУЧАТЕЛЬ</dt>
                <dd>{{ order.recipient }}</dd>
              </div>
              <div class="seller-row">
                <dt>ПОЛУЧЕНИЕ</dt>
                <dd>{{ order.delivery }}</dd>
              </div>
              <div class="seller-row">
                <dt>СУММА</dt>
                <dd class="seller-count">{{ order.total }}</dd>
              </div>
            </dl>
          </div>
          <div class="card-footer">
            <span class="subtle">{{ statusMeta[order.status].hint }}</span>
            <div class="button-row">
              <button
                v-if="statusMeta[order.status].action"
                class="button secondary"
                @click="advance(order)"
              >
                {{ statusMeta[order.status].action
                }}<span class="visually-hidden">: заказ {{ order.number }}</span></button
              ><button class="button text-button" @click="open(order)">
                Открыть заказ<span class="visually-hidden"> {{ order.number }}</span>
              </button>
            </div>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>
