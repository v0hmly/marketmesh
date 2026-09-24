<script setup lang="ts">
import '../style.css';
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import SellerNav from '../components/SellerNav.vue';
import { loadSellerProducts, type SellerProduct, type SellerProductStatus } from '../sample-data';

const session = useSession();
const products = ref<SellerProduct[] | null>(null);
const loading = ref(false);
const failure = ref('');
const feedback = ref('');
const filter = ref<SellerProductStatus | 'all'>('all');
let revision = 0;
let active = true;
const permitted = computed(() => session.state.value.status === 'authenticated');

const filters: { value: SellerProductStatus | 'all'; label: string }[] = [
  { value: 'all', label: 'Все' },
  { value: 'published', label: 'Опубликованы' },
  { value: 'moderation', label: 'На модерации' },
  { value: 'draft', label: 'Черновики' },
  { value: 'hidden', label: 'Скрыты' },
];
const emptyText: Record<SellerProductStatus | 'all', string> = {
  all: 'Изделий пока нет. Начните с черновика карточки.',
  published: 'В продаже пока ничего нет.',
  moderation: 'На модерации ничего нет.',
  draft: 'Черновиков нет.',
  hidden: 'Скрытых изделий нет.',
};
const statusLabel: Record<SellerProductStatus, string> = {
  published: 'Опубликовано',
  moderation: 'На модерации',
  draft: 'Черновик',
  hidden: 'Скрыто',
};
const visible = computed(
  () =>
    products.value?.filter((item) => filter.value === 'all' || item.status === filter.value) ?? [],
);

function clear() {
  revision++;
  products.value = null;
  failure.value = '';
  feedback.value = '';
  filter.value = 'all';
  loading.value = false;
}
async function read() {
  if (loading.value || !permitted.value || !active) return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const value = await loadSellerProducts();
    if (attempt !== revision || !active) return;
    products.value = value;
  } catch {
    if (attempt !== revision) return;
    failure.value =
      'Не удалось загрузить изделия. Мы ничего не меняли в ваших данных. Повторите загрузку.';
  } finally {
    if (attempt === revision) loading.value = false;
  }
}
function add() {
  feedback.value = 'Черновик карточки создан. Заполните описание, цену и фотографии.';
}
function edit(item: SellerProduct) {
  feedback.value = `Карточка «${item.title}» открыта для редактирования.`;
}
function toggle(item: SellerProduct) {
  if (item.status === 'draft') {
    feedback.value = 'Черновик нельзя опубликовать: не хватает обязательных данных.';
    return;
  }
  const next: SellerProductStatus = item.status === 'published' ? 'hidden' : 'published';
  products.value = (products.value ?? []).map((candidate) =>
    candidate.id === item.id ? { ...candidate, status: next } : candidate,
  );
  feedback.value =
    next === 'published'
      ? 'Изделие снова в продаже.'
      : 'Изделие скрыто. Резерв по оформленным заказам сохраняется.';
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
    if (permitted.value && !products.value) void read();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  clear();
});
</script>

<template>
  <section aria-labelledby="seller-products-title">
    <SellerNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ПОРТАЛ ПРОДАВЦА</span>
        <h1 id="seller-products-title">Изделия</h1>
        <p class="lede">
          Карточки готовых изделий и остатки партий. Покупателю видно только доступное количество.
        </p>
      </div>
      <span class="section-number" aria-hidden="true">02 / ИЗДЕЛИЯ</span>
    </div>
    <div v-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Портал продавца начинается со входа</h2>
        <p>Войдите, чтобы увидеть свои изделия.</p>
        <RouterLink class="button primary" to="/seller/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!products" class="card state-card" :aria-busy="loading">
      <template v-if="loading"
        ><span class="loading-dot" aria-hidden="true"></span>
        <p role="status">Загружаем изделия…</p></template
      >
      <template v-else
        ><h2>Изделия пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="sample-content">
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div class="favorites-toolbar card-heading">
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
        <button type="button" class="button primary" @click="add">
          <span>Добавить изделие</span>
          <span aria-hidden="true">↗</span>
        </button>
      </div>
      <p v-if="!visible.length" class="card sample-empty">{{ emptyText[filter] }}</p>
      <ul v-else class="sample-list" aria-label="Список изделий">
        <li v-for="item in visible" :key="item.id" class="card sample-card">
          <div class="card-heading order-heading">
            <div class="order-title">
              <h2>{{ item.title }}</h2>
              <span class="draft-badge">{{ statusLabel[item.status] }}</span>
            </div>
          </div>
          <p class="subtle seller-product-variant">{{ item.variant }}</p>
          <dl class="seller-product-stats">
            <div class="seller-stat">
              <dt>ЦЕНА</dt>
              <dd class="seller-count">{{ item.price || 'Цена не указана' }}</dd>
            </div>
            <div class="seller-stat">
              <dt>ДОСТУПНО</dt>
              <dd class="seller-count">{{ item.available }}</dd>
            </div>
            <div class="seller-stat">
              <dt>В РЕЗЕРВЕ</dt>
              <dd class="seller-count">{{ item.reserved }}</dd>
            </div>
          </dl>
          <p v-if="item.note" class="field-help">{{ item.note }}</p>
          <div class="card-footer">
            <div class="button-row">
              <button class="button secondary" @click="edit(item)">
                Изменить<span class="visually-hidden">: {{ item.title }}</span></button
              ><button class="button text-button" @click="toggle(item)">
                {{ item.status === 'published' ? 'Снять с продажи' : 'Опубликовать'
                }}<span class="visually-hidden">: {{ item.title }}</span>
              </button>
            </div>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>
