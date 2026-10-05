<script setup lang="ts">
import '../style.css';
import ConfirmDialog from '@marketmesh/design-system/ConfirmDialog.vue';
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { ordersEnabled, sellerEnabled } from '../../../shared/features';
import { useSession } from '../../../shell/context';
import StorefrontFeedback from '../components/StorefrontFeedback.vue';
import {
  loadSampleBatches,
  sampleCategories,
  type SampleBatch,
  type SampleCategory,
} from '../sample-data';
import {
  batchDateText,
  keepUnits,
  lowStock,
  money,
  quoted,
  shopName,
  stockText,
  useStorefront,
} from '../store';

type Sort = 'new' | 'cheap' | 'expensive';
const pageSize = 8;

const session = useSession();
const router = useRouter();
const store = useStorefront();
const items = ref<SampleBatch[] | null>(null);
const loading = ref(false);
const failure = ref('');
const category = ref<'all' | SampleCategory>('all');
const sort = ref<Sort>('new');
const more = ref(false);
const favorites = ref<string[]>([]);
const notified = ref<string[]>([]);
let active = true;

const signedIn = computed(() =>
  ['authenticated', 'profilePending'].includes(session.state.value.status),
);
const categories = [{ value: 'all' as const, label: 'Все' }, ...sampleCategories];
const matching = computed(() => {
  const query = store.state.query.toLocaleLowerCase('ru-RU');
  const list = (items.value ?? []).filter(
    (item) =>
      (category.value === 'all' || item.category === category.value) &&
      (!query ||
        item.title.toLocaleLowerCase('ru-RU').includes(query) ||
        item.shop.toLocaleLowerCase('ru-RU').includes(query)),
  );
  if (sort.value === 'cheap') return [...list].sort((a, b) => a.price - b.price);
  if (sort.value === 'expensive') return [...list].sort((a, b) => b.price - a.price);
  return list;
});
const visible = computed(() => (more.value ? matching.value : matching.value.slice(0, pageSize)));
const cartItems = computed(() =>
  (items.value ?? []).filter((item) => store.state.cart.includes(item.id)),
);
const cartTotal = computed(() => money(cartItems.value.reduce((sum, item) => sum + item.price, 0)));
const dialog = computed(() => store.state.dialog);
const cartCountText = computed(() => {
  const n = store.state.cart.length;
  const a = n % 10;
  const b = n % 100;
  const word =
    a === 1 && b !== 11
      ? 'изделие'
      : a >= 2 && a <= 4 && (b < 12 || b > 14)
        ? 'изделия'
        : 'изделий';
  return `В корзине ${n} ${word}`;
});
const ordersPath = computed(() => (signedIn.value && ordersEnabled ? '/account/orders' : '/login'));

async function read() {
  if (loading.value) return;
  loading.value = true;
  failure.value = '';
  try {
    const value = await loadSampleBatches();
    if (!active) return;
    items.value = value;
    store.retainInCart(value.map((item) => item.id));
  } catch {
    if (active)
      failure.value = 'Не удалось загрузить витрину. Мы ничего не меняли. Повторите загрузку.';
  } finally {
    if (active) loading.value = false;
  }
}
/** На телефоне ряд категорий прокручивается: кнопка в фокусе показывается целиком. */
function revealChip(event: FocusEvent) {
  if (event.target instanceof HTMLElement)
    event.target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
function choose(value: 'all' | SampleCategory) {
  category.value = value;
  more.value = false;
  store.announce('');
}
/** После «Показать ещё» фокус переходит к первой новой карточке, а не теряется. */
async function showMore() {
  more.value = true;
  await nextTick();
  document.querySelector<HTMLElement>(`[data-tile-index="${pageSize}"]`)?.focus();
}
function toggleFavorite(item: SampleBatch) {
  if (!signedIn.value) {
    store.askToSignIn(`Войдите, чтобы сохранить ${quoted(item.title)} в избранное.`);
    return;
  }
  const saved = favorites.value.includes(item.id);
  favorites.value = saved
    ? favorites.value.filter((id) => id !== item.id)
    : [...favorites.value, item.id];
  store.announce(
    saved ? `${quoted(item.title)} убрано из избранного.` : `${quoted(item.title)} в избранном.`,
  );
}
function addToCart(item: SampleBatch) {
  // Изделие уже в корзине: кнопка карточки открывает корзину, а не добавляет второй раз.
  if (!store.addToCart(item.id)) {
    store.openCart();
    return;
  }
  // Где хранится корзина гостя, сказано в самой корзине; здесь только факт.
  store.announce(`${quoted(item.title)} в корзине.`);
}
function notify(item: SampleBatch) {
  if (notified.value.includes(item.id)) return;
  if (!signedIn.value) {
    store.askToSignIn(
      `Войдите, чтобы узнать о пополнении партии ${quoted(item.title)}. Письмо придёт на почту вашего аккаунта.`,
    );
    return;
  }
  notified.value = [...notified.value, item.id];
  store.announce('Напишем на почту, когда мастер пополнит партию.');
}
function checkout() {
  if (!cartItems.value.length) {
    store.closeDialog();
    return;
  }
  if (!signedIn.value) {
    store.askToSignIn('Войдите, чтобы оформить заказ. Изделия останутся в корзине этого браузера.');
    return;
  }
  // Оформление честно отвечает внутри корзины: покупатель видит ответ там, где нажал.
  store.setCartNotice(
    'Оформление заказа пока недоступно: мы ещё не принимаем оплату. Корзина сохранена в этом браузере.',
  );
}
async function signIn() {
  store.closeDialog();
  await router.push('/login');
}

watch(
  () => session.state.value.generation,
  () => {
    favorites.value = [];
    notified.value = [];
    store.closeDialog();
  },
);
// Диалог закрылся, а кнопка, открывшая его, исчезла (плашка опустевшей корзины): фокус
// переходит к ленте, а не теряется на body.
watch(
  () => store.state.dialog,
  async (now, before) => {
    if (now || !before) return;
    await nextTick();
    await nextTick();
    if (document.activeElement === document.body)
      document.getElementById('catalog')?.focus({ preventScroll: true });
  },
);
watch(
  () => store.state.query,
  () => {
    more.value = false;
  },
);
void read();
onBeforeUnmount(() => {
  active = false;
  store.closeDialog();
  store.announce('');
});
</script>

<template>
  <div class="storefront">
    <!-- Постоянный live-регион: объявляет действия; видимо их подтверждает кнопка карточки. -->
    <p class="visually-hidden" role="status">{{ store.state.notice }}</p>

    <section id="catalog" class="storefront-feed" aria-labelledby="feed-title" tabindex="-1">
      <div class="storefront-feed-heading">
        <div class="storefront-heading">
          <h1 id="feed-title">Новые партии от мастеров.</h1>
          <p class="lede">
            Готовые изделия небольшими партиями, опубликованные за последнюю неделю.
          </p>
        </div>
      </div>

      <!-- Сначала «что смотреть», потом «в каком порядке» — так и в DOM, и на экране (MM-131). -->
      <div class="storefront-controls">
        <div class="filter-row" role="group" aria-label="Категория" @focusin="revealChip">
          <button
            v-for="item in categories"
            :key="item.value"
            type="button"
            class="button secondary"
            :aria-pressed="category === item.value"
            @click="choose(item.value)"
          >
            {{ item.label }}
          </button>
        </div>
        <div class="field storefront-sort">
          <label for="storefront-sort">Порядок</label>
          <span class="select-wrap">
            <select id="storefront-sort" v-model="sort">
              <option value="new">Сначала новые</option>
              <option value="cheap">Сначала дешевле</option>
              <option value="expensive">Сначала дороже</option>
            </select>
          </span>
        </div>
      </div>

      <div v-if="store.state.query" class="storefront-search-status">
        <h2>Поиск: «{{ store.state.query }}»</h2>
        <p>
          Найдено: {{ matching.length }}.
          <button type="button" class="button text-button" @click="store.search('')">
            Сбросить поиск
          </button>
        </p>
      </div>

      <div v-if="!items" class="card state-card" :aria-busy="loading">
        <template v-if="loading || !failure"
          ><span class="loading-dot" aria-hidden="true"></span>
          <p role="status">Загружаем витрину…</p></template
        >
        <template v-else
          ><p class="notice error" role="alert">{{ failure }}</p>
          <button type="button" class="button secondary" @click="read">
            Повторить загрузку
          </button></template
        >
      </div>
      <div v-else-if="!matching.length" class="card state-card">
        <h2>
          {{ store.state.query ? 'Ничего не нашли' : 'В этой категории новых партий пока нет' }}
        </h2>
        <p>
          {{
            store.state.query
              ? 'Проверьте написание или поищите по названию мастерской.'
              : 'Загляните позже или посмотрите другие категории.'
          }}
        </p>
      </div>
      <ul v-else class="storefront-grid" aria-label="Партии">
        <li
          v-for="(item, index) in visible"
          :key="item.id"
          :data-tile-index="index"
          tabindex="-1"
          class="card storefront-tile"
          :class="{ 'storefront-sold-out': item.left === 0 }"
        >
          <div class="storefront-photo">
            <span class="sample-photo" aria-hidden="true">ФОТО</span>
            <button
              type="button"
              class="storefront-favorite"
              :aria-pressed="favorites.includes(item.id)"
              :aria-label="`В избранное: ${item.title}`"
              @click="toggleFavorite(item)"
            >
              <svg
                width="20"
                height="20"
                viewBox="0 0 24 24"
                :fill="favorites.includes(item.id) ? 'currentColor' : 'none'"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <path
                  d="M12 20.3s-7.3-4.4-9.3-9C1.5 8.2 3.4 4.8 6.9 4.8c2 0 3.6 1.1 5.1 3 1.5-1.9 3.1-3 5.1-3 3.5 0 5.4 3.4 4.2 6.5-2 4.6-9.3 9-9.3 9z"
                />
              </svg>
            </button>
          </div>
          <!-- Слоты плитки — прямые потомки: subgrid выравнивает их по ряду (MM-133). -->
          <span
            class="storefront-stock"
            :class="{ 'storefront-stock-low': item.left > 0 && item.left <= 2 }"
            >{{ stockText(item.left, item.batch) }}</span
          >
          <span
            class="storefront-tag-meter"
            :class="{
              'storefront-tag-meter-none': item.left === item.batch,
              'storefront-tag-meter-ending': lowStock(item.left, item.batch),
            }"
            aria-hidden="true"
            ><span :style="{ width: `${(item.left / item.batch) * 100}%` }"></span
          ></span>
          <span class="storefront-batch-date">{{ batchDateText(item.published) }}</span>
          <span class="storefront-price">{{ money(item.price) }}</span>
          <h2 class="storefront-title">{{ keepUnits(item.title) }}</h2>
          <span class="storefront-shop"
            ><span class="visually-hidden">Мастерская </span>{{ shopName(item.shop) }}</span
          >
          <button
            v-if="item.left > 0"
            type="button"
            class="button secondary"
            :class="{ 'storefront-in-cart': store.state.cart.includes(item.id) }"
            @click="addToCart(item)"
          >
            <svg
              v-if="store.state.cart.includes(item.id)"
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M5 12.5l4.5 4.5L19 7.5" /></svg
            ><template v-if="store.state.cart.includes(item.id)"
              >В корзине<span class="visually-hidden">, открыть корзину</span></template
            ><template v-else>В корзину</template
            ><span class="visually-hidden">: {{ item.title }}</span>
          </button>
          <button v-else type="button" class="button text-button" @click="notify(item)">
            {{ notified.includes(item.id) ? 'Сообщим о пополнении' : 'Сообщить о пополнении'
            }}<span class="visually-hidden">: {{ item.title }}</span>
          </button>
        </li>
      </ul>
      <button
        v-if="items && !more && matching.length > pageSize"
        type="button"
        class="button secondary storefront-more"
        @click="showMore"
      >
        Показать ещё партии
      </button>
    </section>

    <!-- Телефон (MM-131): шапка с корзиной уезжает при прокрутке, плашка держит корзину под
         рукой, пока она не пуста. Залипает внизу экрана и у конца ленты встаёт на место. -->
    <div v-if="store.state.cart.length" class="storefront-cart-bar">
      <span>{{ cartCountText }}</span>
      <button type="button" class="button primary" @click="store.openCart()">
        Открыть корзину
      </button>
    </div>

    <div class="storefront-footer">
      <div class="storefront-links">
        <nav aria-labelledby="footer-buyers">
          <h2 id="footer-buyers" class="eyebrow">Покупателям</h2>
          <ul>
            <li><RouterLink :to="ordersPath">Мои заказы</RouterLink></li>
            <li v-if="!signedIn"><RouterLink to="/register">Создать аккаунт</RouterLink></li>
          </ul>
        </nav>
        <nav v-if="sellerEnabled" aria-labelledby="footer-makers">
          <h2 id="footer-makers" class="eyebrow">Мастерам</h2>
          <ul>
            <li><RouterLink to="/seller/apply">Открыть мастерскую</RouterLink></li>
            <li><RouterLink to="/seller/login">Портал продавца</RouterLink></li>
          </ul>
        </nav>
      </div>
      <StorefrontFeedback :signed-in="signedIn" />
    </div>

    <ConfirmDialog
      v-if="dialog?.kind === 'signIn'"
      title="Вход в MarketMesh ID"
      :return-focus="store.state.returnFocus"
      confirm-label="Войти"
      cancel-label="Не сейчас"
      @cancel="store.closeDialog()"
      @confirm="signIn"
    >
      <p>{{ dialog.reason }}</p>
      <p>
        Нет аккаунта?
        <RouterLink to="/register" @click="store.closeDialog()">Создать аккаунт</RouterLink>
      </p>
    </ConfirmDialog>
    <ConfirmDialog
      v-else-if="dialog?.kind === 'cart'"
      title="Корзина"
      :return-focus="store.state.returnFocus"
      :confirm-label="cartItems.length ? 'Оформить заказ' : 'Выбрать изделия'"
      cancel-label="Продолжить покупки"
      @cancel="store.closeDialog()"
      @confirm="checkout"
    >
      <!-- Постоянный live-регион диалога: внешний скрыт aria-modal, а вставленный вместе
           с текстом регион скринридеры часто не объявляют. -->
      <p
        role="status"
        :class="store.state.cartNotice ? 'notice storefront-cart-notice' : 'visually-hidden'"
      >
        {{ store.state.cartNotice }}
      </p>
      <p v-if="!cartItems.length">
        Корзина пуста. Добавляйте изделия из витрины — они сохранятся, даже если вы не вошли.
      </p>
      <template v-else>
        <ul class="storefront-cart-list">
          <li v-for="item in cartItems" :key="item.id">
            <span class="sample-photo photo-small" aria-hidden="true"></span>
            <div class="line-meta">
              <span>{{ item.title }}</span>
              <span class="subtle">{{ item.shop }}</span>
            </div>
            <div class="storefront-cart-amount">
              <span class="line-amount">{{ money(item.price) }}</span>
              <button
                type="button"
                class="button text-button"
                @click="store.removeFromCart(item.id)"
              >
                Убрать<span class="visually-hidden">: {{ item.title }}</span>
              </button>
            </div>
          </li>
        </ul>
        <p class="storefront-cart-total">
          <span>Итого без доставки</span><span class="line-amount">{{ cartTotal }}</span>
        </p>
        <p v-if="!signedIn" class="subtle storefront-cart-note">
          Пока вы не вошли, корзина хранится в этом браузере.
        </p>
      </template>
    </ConfirmDialog>
  </div>
</template>
