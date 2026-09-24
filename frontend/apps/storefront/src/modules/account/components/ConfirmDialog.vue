<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, onUnmounted, ref, useId, watch } from 'vue';

/**
 * Подтверждение необратимого действия по компоненту Dialog дизайн-системы:
 * фокус внутри и обратно к кнопке, ловушка фокуса, Escape — отмена,
 * клик по подложке ничего не делает, страница под диалогом не прокручивается.
 */
const props = defineProps<{
  title: string;
  confirmLabel: string;
  cancelLabel: string;
  busy?: boolean;
  /** Куда вернуть фокус после закрытия; по умолчанию — элемент, активный при открытии. */
  returnFocus?: HTMLElement | null;
}>();
const emit = defineEmits<{ cancel: []; confirm: [] }>();
const titleId = useId();
const dialog = ref<HTMLElement | null>(null);
let trigger: HTMLElement | null = null;
let overflow = '';

function focusable(): HTMLElement[] {
  return Array.from(
    dialog.value?.querySelectorAll<HTMLElement>(
      'button:not(:disabled), [href], input:not(:disabled), [tabindex]:not([tabindex="-1"])',
    ) ?? [],
  );
}
function cancel() {
  if (!props.busy) emit('cancel');
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault();
    cancel();
    return;
  }
  if (event.key !== 'Tab') return;
  const items = focusable();
  if (!items.length) {
    event.preventDefault();
    dialog.value?.focus();
    return;
  }
  const first = items[0]!;
  const last = items[items.length - 1]!;
  const current = document.activeElement;
  if (event.shiftKey && (current === first || current === dialog.value)) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && current === last) {
    event.preventDefault();
    first.focus();
  }
}
// Пока действие выполняется, кнопки недоступны: фокус остаётся на самом диалоге, чтобы не
// уйти на страницу, и возвращается к отмене, если диалог остался открытым.
watch(
  () => props.busy,
  async (busy) => {
    await nextTick();
    if (busy) dialog.value?.focus();
    else dialog.value?.querySelector<HTMLElement>('[data-autofocus]')?.focus();
  },
);
/** Фокус, ушедший за пределы диалога (например, щелчком по подложке), возвращается внутрь. */
function keepFocus(event: FocusEvent) {
  if (dialog.value && event.target instanceof Node && !dialog.value.contains(event.target))
    (focusable()[0] ?? dialog.value).focus();
}
onMounted(async () => {
  trigger =
    props.returnFocus ??
    (document.activeElement instanceof HTMLElement ? document.activeElement : null);
  document.addEventListener('focusin', keepFocus);
  overflow = document.body.style.overflow;
  document.body.style.overflow = 'hidden';
  await nextTick();
  (dialog.value?.querySelector<HTMLElement>('[data-autofocus]') ?? dialog.value)?.focus();
});
onBeforeUnmount(() => {
  document.removeEventListener('focusin', keepFocus);
  document.body.style.overflow = overflow;
});
onUnmounted(() => {
  const source = trigger;
  // Кнопка-источник могла быть отключена на время диалога: возвращаем фокус после
  // обновления экрана. Если она исчезла вместе с записью, фокус переводит сам экран.
  void nextTick(() => {
    if (source?.isConnected && !(source as HTMLButtonElement).disabled) source.focus();
  });
});
</script>

<template>
  <div class="dialog-scrim">
    <div
      ref="dialog"
      class="card dialog"
      role="dialog"
      aria-modal="true"
      :aria-labelledby="titleId"
      tabindex="-1"
      @keydown="keydown"
    >
      <h2 :id="titleId">{{ title }}</h2>
      <slot />
      <div class="dialog-actions">
        <button
          type="button"
          class="button text-button"
          data-autofocus
          :disabled="busy"
          @click="cancel"
        >
          {{ cancelLabel }}</button
        ><button type="button" class="button primary" :disabled="busy" @click="emit('confirm')">
          {{ confirmLabel }}
        </button>
      </div>
    </div>
  </div>
</template>
