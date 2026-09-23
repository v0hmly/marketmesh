<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import { useAccountCounts } from '../counts';
import { idEnabled } from '../../../shared/features';
import { publicLineOf } from '../validation';
import { loadSampleReviews, type SampleReview, type SampleWaitingReview } from '../sample-data';

const session = useSession();
const counts = useAccountCounts();
/** Фактическая публичная строка из MarketMesh ID; null — пока не прочитана или без ID. */
const publicLine = ref<string | null>(null);
const waiting = ref<SampleWaitingReview[] | null>(null);
const mine = ref<SampleReview[] | null>(null);
const loading = ref(false);
const failure = ref('');
const feedback = ref('');
const tab = ref<'waiting' | 'mine'>('waiting');
const openId = ref<string | null>(null);
const rating = ref(0);
const text = ref('');
const attempted = ref(false);
let revision = 0;
let active = true;
/** Владелец, для которого прочитаны отзывы и публичная строка. */
let owner: string | null = null;
const permitted = computed(() => session.state.value.status === 'authenticated');

const ratingHints = [
  'Выберите оценку',
  'Совсем не подошло',
  'Так себе',
  'Нормально',
  'Хорошо',
  'Отлично',
];
const publicHint = computed(() =>
  publicLine.value
    ? `Рядом с отзывом покажем: ${publicLine.value}.`
    : idEnabled
      ? 'Рядом с отзывом покажем ваше имя и город.'
      : 'Рядом с отзывом покажем ваше имя.',
);
const textCount = computed(() => Array.from(text.value).length);
const errors = computed<{ rating?: string; text?: string }>(() => {
  if (!attempted.value) return {};
  const result: { rating?: string; text?: string } = {};
  if (rating.value === 0) result.rating = 'Поставьте оценку — без неё отзыв не отправить.';
  if (Array.from(text.value.trim()).length < 10)
    result.text = 'Напишите хотя бы одно предложение: так отзыв поможет другим покупателям.';
  return result;
});

function clear() {
  revision++;
  waiting.value = null;
  mine.value = null;
  failure.value = '';
  feedback.value = '';
  tab.value = 'waiting';
  publicLine.value = null;
  openId.value = null;
  rating.value = 0;
  text.value = '';
  attempted.value = false;
  loading.value = false;
}
/** Подсказку под формой строим из MarketMesh ID; без него остаётся общая формулировка. */
async function readPublicLine() {
  if (!idEnabled || !permitted.value || !active) return;
  const attempt = revision;
  try {
    const profile = await session.readProfile(session.capture());
    if (attempt === revision && active) publicLine.value = publicLineOf(profile);
  } catch {
    /* The generic hint stays; the review form does not depend on the profile. */
  }
}
async function read() {
  if (loading.value || !permitted.value || !active) return;
  const attempt = revision;
  owner = session.state.value.subjectId;
  if (publicLine.value === null) void readPublicLine();
  loading.value = true;
  failure.value = '';
  feedback.value = '';
  try {
    const value = await loadSampleReviews();
    if (attempt !== revision || !active) return;
    waiting.value = value.waiting;
    mine.value = value.mine;
    if (counts) counts.reviews = value.waiting.length;
  } catch {
    if (attempt !== revision) return;
    failure.value =
      'Не удалось загрузить отзывы. Мы ничего не меняли в ваших данных. Повторите загрузку.';
  } finally {
    if (attempt === revision) loading.value = false;
  }
}
async function open(item: SampleWaitingReview) {
  openId.value = item.id;
  rating.value = 0;
  text.value = '';
  attempted.value = false;
  feedback.value = '';
  await nextTick();
  document.getElementById(`rate-${item.id}-1`)?.focus();
}
function cancel() {
  openId.value = null;
  rating.value = 0;
  text.value = '';
  attempted.value = false;
}
async function submit(item: SampleWaitingReview) {
  attempted.value = true;
  feedback.value = '';
  if (errors.value.rating || errors.value.text) {
    await nextTick();
    document.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    return;
  }
  const review: SampleReview = {
    id: `new-${item.id}`,
    title: item.title,
    rating: rating.value,
    date: 'СЕГОДНЯ',
    status: 'На модерации',
    text: text.value.trim(),
    reply: '',
  };
  waiting.value = waiting.value?.filter((candidate) => candidate.id !== item.id) ?? [];
  if (counts) counts.reviews = waiting.value.length;
  mine.value = [review, ...(mine.value ?? [])];
  openId.value = null;
  rating.value = 0;
  text.value = '';
  attempted.value = false;
  feedback.value = 'Отзыв отправлен. Мы опубликуем его после модерации и сообщим на почту.';
}
function edit() {
  feedback.value = 'Отзыв открыт для правки. После изменения он снова пройдёт модерацию.';
}
function remove(review: SampleReview) {
  mine.value = mine.value?.filter((candidate) => candidate.id !== review.id) ?? [];
  feedback.value = 'Отзыв удалён.';
}
watch(() => session.state.value.generation, clear, { flush: 'sync' });
// Смена владельца в том же поколении (восстановление сессии другим аккаунтом) тоже стирает
// данные прежнего: публичная строка — его имя и город.
watch(
  () => session.state.value.subjectId,
  (subject) => {
    if (subject && owner && subject !== owner) clear();
  },
  { flush: 'sync' },
);
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
    if (permitted.value && !waiting.value) void read();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  active = false;
  clear();
});
</script>

<template>
  <section class="account-section" aria-labelledby="reviews-title">
    <div class="account-heading">
      <h1 id="reviews-title">Отзывы</h1>
      <p class="lede">Мастеру важно услышать вас, а другим покупателям — увидеть настоящий опыт.</p>
    </div>
    <div v-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы увидеть и оставить отзывы.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!waiting || !mine" class="card state-card" :aria-busy="loading">
      <template v-if="loading"
        ><span class="loading-dot" aria-hidden="true"></span>
        <p role="status">Загружаем отзывы…</p></template
      >
      <template v-else
        ><h2>Отзывы пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="sample-content">
      <p v-if="feedback" class="notice success" role="status">{{ feedback }}</p>
      <div class="filter-row" role="group" aria-label="Разделы отзывов">
        <button
          type="button"
          class="button secondary"
          :aria-pressed="tab === 'waiting'"
          @click="tab = 'waiting'"
        >
          Ждут отзыва · {{ waiting.length }}
        </button>
        <button
          type="button"
          class="button secondary"
          :aria-pressed="tab === 'mine'"
          @click="tab = 'mine'"
        >
          Мои отзывы · {{ mine.length }}
        </button>
      </div>
      <template v-if="tab === 'waiting'">
        <div v-if="!waiting.length" class="card state-card">
          <h2>Всё рассказано</h2>
          <p>Вы оставили отзывы на все полученные изделия. Спасибо — мастерам это правда важно.</p>
        </div>
        <ul v-else class="sample-list" aria-label="Изделия, которые ждут отзыва">
          <li v-for="item in waiting" :key="item.id" class="card sample-card">
            <div class="review-line">
              <span class="sample-photo photo-thumb" aria-hidden="true">ФОТО</span>
              <span class="line-meta"
                ><h2 class="review-title">{{ item.title }}</h2>
                <span class="subtle">{{ item.meta }}</span></span
              >
              <button v-if="openId !== item.id" class="button secondary" @click="open(item)">
                Написать отзыв<span class="visually-hidden"> на «{{ item.title }}»</span>
              </button>
            </div>
            <form
              v-if="openId === item.id"
              class="review-form"
              novalidate
              @submit.prevent="submit(item)"
            >
              <fieldset class="star-pick">
                <legend>Оценка</legend>
                <div class="star-row">
                  <span v-for="n in 5" :key="n" class="star-option">
                    <input
                      :id="`rate-${item.id}-${n}`"
                      v-model.number="rating"
                      type="radio"
                      :name="`rating-${item.id}`"
                      :value="n"
                      :aria-invalid="Boolean(errors.rating)"
                      :aria-describedby="errors.rating ? `rating-${item.id}-error` : undefined"
                    />
                    <label :for="`rate-${item.id}-${n}`">
                      <svg
                        width="28"
                        height="28"
                        viewBox="0 0 24 24"
                        aria-hidden="true"
                        class="star-icon"
                        :class="{ 'star-off': n > rating }"
                        :fill="n <= rating ? 'currentColor' : 'none'"
                        stroke="currentColor"
                        stroke-width="1.4"
                        stroke-linejoin="round"
                      >
                        <path
                          d="M12 3.6l2.6 5.5 6 .8-4.4 4.2 1.1 6-5.3-2.9-5.3 2.9 1.1-6L3.4 9.9l6-.8z"
                        />
                      </svg>
                      <span class="visually-hidden">{{ n }} из 5</span>
                    </label>
                  </span>
                  <span class="subtle rating-hint">{{ ratingHints[rating] }}</span>
                </div>
                <span v-if="errors.rating" :id="`rating-${item.id}-error`" class="field-error">{{
                  errors.rating
                }}</span>
              </fieldset>
              <div class="field">
                <div class="label-line">
                  <label :for="`text-${item.id}`">Что скажете об изделии</label
                  ><span :id="`count-${item.id}`" class="field-count">{{ textCount }} / 2000</span>
                </div>
                <textarea
                  :id="`text-${item.id}`"
                  v-model="text"
                  rows="4"
                  maxlength="2000"
                  :aria-invalid="Boolean(errors.text)"
                  :aria-describedby="`help-${item.id} count-${item.id}${errors.text ? ` text-${item.id}-error` : ''}`"
                ></textarea>
                <span :id="`help-${item.id}`" class="field-help"
                  >Опишите, каким изделие пришло и как им пользуетесь. Отзыв проходит
                  модерацию.</span
                >
                <span v-if="errors.text" :id="`text-${item.id}-error`" class="field-error">{{
                  errors.text
                }}</span>
              </div>
              <div class="form-footer">
                <span class="field-help"
                  >{{ publicHint
                  }}<template v-if="idEnabled">
                    Изменить — <RouterLink to="/account/id">в MarketMesh ID</RouterLink>.</template
                  ></span
                >
                <div class="button-row">
                  <button class="button secondary" type="button" @click="cancel">Отменить</button
                  ><button class="button primary" type="submit">
                    Отправить отзыв <span aria-hidden="true">↗</span>
                  </button>
                </div>
              </div>
            </form>
          </li>
        </ul>
      </template>
      <template v-else>
        <div v-if="!mine.length" class="card state-card">
          <h2>Отзывов пока нет</h2>
          <p>Они появятся здесь после того, как вы расскажете о полученных изделиях.</p>
        </div>
        <ul v-else class="sample-list" aria-label="Мои отзывы">
          <li v-for="review in mine" :key="review.id" class="card sample-card">
            <div class="card-heading">
              <div class="review-line">
                <span class="sample-photo photo-small" aria-hidden="true">ФОТО</span>
                <span class="line-meta"
                  ><h2 class="review-title">{{ review.title }}</h2>
                  <span class="review-meta">
                    <span class="stars" role="img" :aria-label="`Оценка ${review.rating} из 5`">
                      <svg
                        v-for="n in 5"
                        :key="n"
                        width="18"
                        height="18"
                        viewBox="0 0 24 24"
                        aria-hidden="true"
                        class="star-icon"
                        :class="{ 'star-off': n > review.rating }"
                        :fill="n <= review.rating ? 'currentColor' : 'none'"
                        stroke="currentColor"
                        stroke-width="1.4"
                        stroke-linejoin="round"
                      >
                        <path
                          d="M12 3.6l2.6 5.5 6 .8-4.4 4.2 1.1 6-5.3-2.9-5.3 2.9 1.1-6L3.4 9.9l6-.8z"
                        />
                      </svg> </span
                    ><span class="review-date">{{ review.date }}</span></span
                  ></span
                >
              </div>
              <span class="draft-badge">{{ review.status }}</span>
            </div>
            <p class="review-text">{{ review.text }}</p>
            <div v-if="review.reply" class="reply-panel">
              <span class="reply-label">ОТВЕТ МАСТЕРА</span>
              <p>{{ review.reply }}</p>
            </div>
            <div class="button-row">
              <button class="button secondary" @click="edit">
                Изменить<span class="visually-hidden"> отзыв на «{{ review.title }}»</span></button
              ><button class="button text-button" @click="remove(review)">
                Удалить отзыв<span class="visually-hidden"> на «{{ review.title }}»</span>
              </button>
            </div>
          </li>
        </ul>
      </template>
    </div>
  </section>
</template>
