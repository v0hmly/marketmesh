<script setup lang="ts">
import '../style.css';

import ConfirmDialog from '@marketmesh/design-system/ConfirmDialog.vue';
import { useAddressBook } from './state';
const {
  session,
  book,
  latest,
  draft,
  editing,
  editingId,
  deleting,
  deleteTrigger,
  loading,
  saving,
  reconcile,
  unknownCreate,
  failure,
  feedback,
  pending,
  errors,
  attempts,
  delays,
  permitted,
  busy,
  dirty,
  shown,
  idKey,
  editableLatest,
  read,
  retry,
  start,
  cancel,
  mutate,
  acceptLatest,
  keepDraft,
  requestDelete,
  addressFields,
} = useAddressBook();
</script>

<template>
  <section class="account-section" aria-labelledby="addresses-title">
    <div class="account-heading">
      <h1 id="addresses-title">Адреса доставки</h1>
      <p class="lede">Сохраните удобные адреса, чтобы они были под рукой при оформлении заказа.</p>
    </div>
    <div v-if="pending || session.state.value.status === 'profilePending'" class="card state-card">
      <h2>Готовим вашу адресную книгу</h2>
      <p role="status">Профиль создаётся. Данные появятся после подготовки аккаунта.</p>
      <p v-if="attempts >= delays.length">Автоматическая проверка приостановлена.</p>
      <button class="button secondary" :disabled="busy" @click="retry">Проверить готовность</button>
    </div>
    <div v-else-if="!permitted" class="card state-card">
      <p v-if="['unknown', 'checking'].includes(session.state.value.status)" role="status">
        Проверяем сессию…
      </p>
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите, чтобы открыть свои адреса.</p>
        <RouterLink class="button primary" to="/login">Перейти ко входу</RouterLink></template
      >
    </div>
    <div v-else-if="!book" class="card state-card" :aria-busy="loading">
      <p v-if="loading" role="status">Загружаем адреса…</p>
      <template v-else
        ><h2>Адреса пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="read()">Повторить загрузку</button></template
      >
    </div>
    <div v-else class="address-content" :aria-busy="busy">
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
      <p v-if="feedback" id="addresses-result" class="notice success" role="status" tabindex="-1">
        {{ feedback }}
      </p>
      <div v-if="reconcile" class="reconcile-panel" aria-labelledby="address-reconcile-title">
        <h2 id="address-reconcile-title">Сверим адресную книгу</h2>
        <p v-if="unknownCreate">
          Новый адрес мог уже сохраниться. Проверьте список, чтобы не создать дубликат.
        </p>
        <button class="button secondary" :disabled="busy" @click="read(true)">
          Перечитать актуальные данные
        </button>
        <template v-if="latest">
          <p>Ниже показан актуальный список. Ваш черновик остаётся в форме.</p>
          <p v-if="editing && !editableLatest" role="status">
            Редактируемый адрес удалён. Этот черновик нельзя сохранить поверх другой записи.
          </p>
          <div class="button-row">
            <button class="button secondary" :disabled="busy" @click="acceptLatest">
              Принять актуальную книгу
            </button>
            <button
              v-if="editing && editableLatest && (editingId || latest.addresses.length < 20)"
              class="button text-button"
              :disabled="busy"
              @click="keepDraft"
            >
              {{
                unknownCreate
                  ? 'Проверил список: подготовить ещё один адрес'
                  : 'Оставить мой черновик для сохранения'
              }}
            </button>
          </div>
        </template>
      </div>
      <div class="address-toolbar">
        <p class="subtle">
          {{ shown?.addresses.length }} / 20 адресов. Контакты доступны только вам.
        </p>
        <button
          id="add-address"
          class="button primary"
          :disabled="
            busy || editing || Boolean(deleting) || reconcile || book.addresses.length >= 20
          "
          @click="start()"
        >
          Добавить адрес <span aria-hidden="true">↗</span>
        </button>
      </div>
      <p v-if="!shown?.addresses.length" class="card address-empty">
        Пока нет сохранённых адресов. Первый адрес станет основным.
      </p>
      <ul v-else class="address-list" aria-label="Сохранённые адреса">
        <li v-for="address in shown.addresses" :key="idKey(address.addressId)" class="address-row">
          <div class="address-head">
            <h2>{{ address.fields?.recipient }}</h2>
            <span v-if="address.isDefault" class="draft-badge">По умолчанию</span>
          </div>
          <address>
            {{ address.fields?.phone }}<br />{{
              [
                address.fields?.country,
                address.fields?.postalCode,
                address.fields?.city,
                address.fields?.streetHouse,
                address.fields?.apartment,
              ]
                .filter(Boolean)
                .join(', ')
            }}
          </address>
          <p v-if="address.fields?.comment" class="plain-text address-comment">
            {{ address.fields.comment }}
          </p>
          <div class="address-actions">
            <button
              class="button secondary"
              :disabled="busy || editing || Boolean(deleting) || reconcile"
              @click="start(address)"
            >
              Изменить<span class="visually-hidden">
                адрес {{ address.fields?.recipient }}</span
              ></button
            ><button
              v-if="!address.isDefault"
              class="button text-button"
              :disabled="busy || editing || Boolean(deleting) || reconcile"
              @click="mutate('default', address)"
            >
              Использовать по умолчанию<span class="visually-hidden"
                >: {{ address.fields?.recipient }}</span
              ></button
            ><button
              class="button text-button"
              :disabled="busy || editing || Boolean(deleting) || reconcile"
              @click="requestDelete(address, $event)"
            >
              Удалить<span class="visually-hidden"> адрес {{ address.fields?.recipient }}</span>
            </button>
          </div>
        </li>
      </ul>
      <ConfirmDialog
        v-if="deleting && !reconcile"
        :title="`Удалить адрес «${deleting.fields?.recipient ?? ''}»?`"
        cancel-label="Оставить адрес"
        confirm-label="Удалить адрес"
        :busy="busy"
        :return-focus="deleteTrigger"
        @cancel="deleting = null"
        @confirm="mutate('delete', deleting)"
      >
        <p>
          {{ deleting.fields?.city }}, {{ deleting.fields?.streetHouse }}. Остальные адреса
          останутся без изменений.
        </p>
        <p v-if="deleting.isDefault">Другой основной адрес не будет выбран автоматически.</p>
      </ConfirmDialog>
      <div v-if="editing" class="card profile-card address-editor">
        <h2>{{ editingId ? 'Изменить адрес' : 'Новый адрес' }}</h2>
        <p class="subtle">
          Обязательные поля отмечены *. Телефон нужен для получения; он не меняет логин.
        </p>
        <form novalidate :aria-busy="saving" @submit.prevent="mutate('save')">
          <div v-for="field in addressFields" :key="field.key" class="field">
            <label :for="`address-${field.key}`"
              >{{ field.label }}<span v-if="field.required" aria-hidden="true"> *</span></label
            >
            <textarea
              v-if="field.key === 'comment'"
              :id="`address-${field.key}`"
              v-model="draft[field.key]"
              rows="3"
              :disabled="busy"
              :aria-invalid="Boolean(errors[field.key])"
              :aria-describedby="`address-${field.key}-help${errors[field.key] ? ` address-${field.key}-error` : ''}`"
            ></textarea>
            <input
              v-else
              :id="`address-${field.key}`"
              v-model="draft[field.key]"
              :type="field.key === 'phone' ? 'tel' : 'text'"
              :autocomplete="field.autocomplete"
              :required="field.required"
              :disabled="busy"
              :aria-invalid="Boolean(errors[field.key])"
              :aria-describedby="`address-${field.key}-help${errors[field.key] ? ` address-${field.key}-error` : ''}`"
            />
            <span :id="`address-${field.key}-help`" class="field-help"
              >До {{ field.limit }} символов{{ field.required ? '' : ', необязательно' }}.</span
            >
            <span v-if="errors[field.key]" :id="`address-${field.key}-error`" class="field-error">{{
              errors[field.key]
            }}</span>
          </div>
          <div class="form-footer">
            <span class="field-help">{{
              dirty ? 'Есть несохранённые изменения' : 'Проверьте адрес перед сохранением'
            }}</span>
            <div class="button-row">
              <button
                class="button secondary"
                type="button"
                :disabled="busy || reconcile"
                @click="cancel"
              >
                Отменить</button
              ><button class="button primary" type="submit" :disabled="busy || reconcile">
                {{ saving ? 'Сохраняем…' : 'Сохранить адрес' }}
              </button>
            </div>
          </div>
        </form>
      </div>
    </div>
  </section>
</template>
