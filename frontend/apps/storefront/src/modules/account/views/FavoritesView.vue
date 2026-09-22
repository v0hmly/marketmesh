<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import AccountNav from '../components/AccountNav.vue';
import { loadSampleFavorites, type SampleFavorite } from '../sample-data';

const session = useSession();
const items = ref<SampleFavorite[] | null>(null);
const loading = ref(false);
const failure = ref('');
const feedback = ref('');
const filter = ref<'all' | 'in' | 'out'>('all');
const notified = ref<string[]>([]);
let revision = 0;
let active = true;
const permitted = computed(() => session.state.value.status === 'authenticated');

const filters: { value: 'all' | 'in' | 'out'; label: string }[] = [
  { value: 'all', label: 'Все' },
  { value: 'in', label: 'В наличии' },
  { value: 'out', label: 'Закончились' },
];
const emptyText: Record<'all' | 'in' | 'out', { title: string; text: string }> = {
  all: {
    title: 'В избранном пусто',
    text: 'Отмечайте изделия сердцем в каталоге — они соберутся здесь.',
  },
  in: {
    title: 'Ничего нет в наличии',
    text: 'Все отмеченные изделия сейчас распроданы. Мы сообщим о пополнении.',
  },
  out: { title: 'Всё в наличии', text: 'Ни одно изделие из избранного не закончилось.' },
};
const visible = computed(
  () =>
    items.value?.filter((item) => {
      if (filter.value === 'in') return item.left > 0;
      if (filter.value === 'out') return item.left === 0;
      return true;
    }) ?? [],
);
const stockText = (item: SampleFavorite) =>
  item.left === 0 ? 'Партия закончилась' : item.left <= 2 ? `Осталось ${item.left}` : 'В наличии';

function clear() {
  revision++;
  items.value = null;
  failure.value = '';
  feedback.value = '';
  filter.value = 'all';
  notified.value = [];
  loading.value = false;
}
async function read() {
  if (loading.value || !permitted.value || !active) return;
  const attempt = revision;
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const value = await loadSampleFavorites();
    if (attempt !== revision || !active) return;
    items.value = value;
  } catch {
    if (attempt !== revision) return;
    failure.value =
      'Не удалось загрузить избранное. Мы ничего не меняли в ваших данных. Повторите загрузку.';
  } finally {
    if (attempt === revision) loading.value = false;
  }
}
function add(item: SampleFavorite) {
  feedback.value = `«${item.title}» в корзине.`;
}
function notify(item: SampleFavorite) {
  if (notified.value.includes(item.id)) return;
  notified.value = [...notified.value, item.id];
  feedback.value = 'Напишем на почту, когда мастер пополнит партию.';
}
function remove(item: SampleFavorite) {
  items.value = items.value?.filter((candidate) => candidate.id !== item.id) ?? [];
  feedback.value = `«${item.title}» убрано из избранного.`;
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
    if (permitted.value && !items.value) void read();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  clear();
});
</script>

<template>
  <section aria-labelledby="favorites-title">
    <AccountNav />
    <div class="page-heading">
      <div>
        <span class="eyebrow">ЛИЧНЫЙ КАБИНЕТ</span>
        <h1 id="favorites-title">Избранное</h1>
        <p class="lede">
          Изделия, к которым вы хотите вернуться. Мы предупредим, когда мастер пополнит партию.
        </p>
      </div>
      <span class="section-number" aria-hidden="true">05 / ИЗБРАННОЕ</span>
    </div>
    <div v-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы увидеть своё избранное.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!items" class="card state-card" :aria-busy="loading">
      <template v-if="loading"
        ><span class="loading-dot" aria-hidden="true"></span>
        <p role="status">Загружаем избранное…</p></template
      >
      <template v-else
        ><h2>Избранное пока недоступно</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="sample-content">
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div class="address-toolbar favorites-toolbar">
        <div class="filter-row" role="group" aria-label="Фильтр избранного">
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
        <p class="subtle">{{ items.length }} изделий в избранном</p>
      </div>
      <div v-if="!visible.length" class="card state-card">
        <h2>{{ emptyText[filter].title }}</h2>
        <p>{{ emptyText[filter].text }}</p>
      </div>
      <ul v-else class="favorites-grid" aria-label="Избранные изделия">
        <li v-for="item in visible" :key="item.id" class="card favorite-card">
          <span class="sample-photo photo-tile" aria-hidden="true">ФОТО ИЗДЕЛИЯ</span>
          <div class="favorite-description">
            <h2>{{ item.title }}</h2>
            <span class="subtle">{{ item.maker }}</span>
          </div>
          <div class="favorite-price">
            <span class="order-total">{{ item.price }}</span>
            <span class="subtle" :class="{ 'stock-out': item.left === 0 }">{{
              stockText(item)
            }}</span>
          </div>
          <div class="favorite-actions">
            <button v-if="item.left > 0" class="button primary wide" @click="add(item)">
              В корзину <span aria-hidden="true">↗</span
              ><span class="visually-hidden">: {{ item.title }}</span>
            </button>
            <button v-else class="button secondary" @click="notify(item)">
              {{ notified.includes(item.id) ? 'Сообщим о пополнении' : 'Сообщить о пополнении'
              }}<span class="visually-hidden">: {{ item.title }}</span>
            </button>
            <button class="button text-button" @click="remove(item)">
              Убрать из избранного<span class="visually-hidden">: {{ item.title }}</span>
            </button>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>
