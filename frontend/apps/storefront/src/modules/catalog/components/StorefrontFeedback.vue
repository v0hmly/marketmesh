<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useSession } from '../../../shell/context';
import { sampleOrders, sendSampleFeedback } from '../sample-data';
import { useStorefront } from '../store';

const props = defineProps<{ signedIn: boolean }>();
const store = useStorefront();
const session = useSession();
const limit = 2000;
const topic = ref<'idea' | 'bug' | 'order' | 'other'>('idea');
const order = ref('');
const message = ref('');
const busy = ref(false);
const attempted = ref(false);
const ticket = ref('');
let active = true;

const orderError = computed(() =>
  attempted.value && topic.value === 'order' && !order.value
    ? 'Заказ не выбран. Выберите его из списка — так поддержка сразу увидит детали.'
    : '',
);
const messageError = computed(() =>
  attempted.value && !message.value.trim()
    ? 'Сообщение пустое. Опишите вопрос или идею хотя бы парой предложений.'
    : '',
);
const showCount = computed(() => limit - message.value.length <= 200);
const messageDescribedBy = computed(() =>
  [
    'feedback-message-help',
    messageError.value && 'feedback-message-error',
    showCount.value && 'feedback-message-count',
  ]
    .filter(Boolean)
    .join(' '),
);

async function submit() {
  if (busy.value) return;
  attempted.value = true;
  if (orderError.value || messageError.value) return;
  busy.value = true;
  try {
    const number = await sendSampleFeedback();
    if (!active) return;
    ticket.value = number;
  } finally {
    if (active) busy.value = false;
  }
}
function again() {
  topic.value = 'idea';
  order.value = '';
  message.value = '';
  attempted.value = false;
  ticket.value = '';
}
// Черновик и номер обращения принадлежат сеансу: смена аккаунта начинает форму заново.
watch(() => session.state.value.generation, again);
onBeforeUnmount(() => {
  active = false;
});
</script>

<template>
  <section class="storefront-feedback" aria-labelledby="feedback-title">
    <div>
      <h2 id="feedback-title">Напишите нам</h2>
      <p class="subtle">
        {{
          props.signedIn
            ? 'Ответим на почту вашего MarketMesh ID.'
            : 'Ответим на почту вашего MarketMesh ID. Чтобы написать, войдите — так поддержка сразу увидит ваши заказы.'
        }}
      </p>
    </div>
    <button
      v-if="!props.signedIn"
      type="button"
      class="button secondary"
      @click="
        store.askToSignIn(
          'Войдите, чтобы написать в поддержку. Ответ придёт на почту вашего MarketMesh ID.',
        )
      "
    >
      Войти и написать
    </button>
    <div v-else-if="ticket" class="storefront-feedback-sent">
      <p class="notice success" role="status">
        Сообщение отправлено, номер обращения — {{ ticket }}. Ответ придёт на почту вашего
        MarketMesh ID.
      </p>
      <button type="button" class="button text-button" @click="again">
        Написать ещё одно сообщение
      </button>
    </div>
    <form
      v-else
      novalidate
      aria-labelledby="feedback-title"
      :aria-busy="busy"
      @submit.prevent="submit"
    >
      <div class="field">
        <label for="feedback-topic">Тема</label>
        <span class="select-wrap">
          <select id="feedback-topic" v-model="topic">
            <option value="idea">Идея или пожелание</option>
            <option value="bug">Что-то работает не так</option>
            <option value="order">Вопрос по заказу</option>
            <option value="other">Другое</option>
          </select>
        </span>
      </div>
      <div v-if="topic === 'order'" class="field">
        <label for="feedback-order">Заказ</label>
        <span class="select-wrap">
          <select
            id="feedback-order"
            v-model="order"
            :aria-invalid="Boolean(orderError)"
            :aria-describedby="orderError ? 'feedback-order-error' : undefined"
          >
            <option value="">Выберите заказ</option>
            <option v-for="item in sampleOrders" :key="item.value" :value="item.value">
              {{ item.label }}
            </option>
          </select>
        </span>
        <span v-if="orderError" id="feedback-order-error" class="field-error">{{
          orderError
        }}</span>
      </div>
      <div class="field">
        <div class="label-line">
          <label for="feedback-message">Сообщение</label>
          <span v-if="showCount" id="feedback-message-count" class="field-count"
            >{{ message.length }} / {{ limit }}</span
          >
        </div>
        <textarea
          id="feedback-message"
          v-model="message"
          rows="4"
          :maxlength="limit"
          :aria-invalid="Boolean(messageError)"
          :aria-describedby="messageDescribedBy"
        ></textarea>
        <span id="feedback-message-help" class="field-help"
          >Если пишете об изделии или мастерской, укажите название — так ответим быстрее.</span
        >
        <span v-if="messageError" id="feedback-message-error" class="field-error">{{
          messageError
        }}</span>
      </div>
      <button type="submit" class="button secondary" :disabled="busy">
        {{ busy ? 'Отправляем…' : 'Отправить' }}
      </button>
    </form>
  </section>
</template>
