<script setup lang="ts">
import '../style.css';

import { AccountSecurity } from '../../auth/ui';

import AvatarEditor from '../avatar/AvatarEditor.vue';
import { computed, ref } from 'vue';
import ConfirmDialog from '@marketmesh/design-system/ConfirmDialog.vue';
import { useIdentityEditor } from './state';
/** Идёт запрос фото: выход из сеанса прервал бы его, остальное на странице ему не мешает. */
const avatarActive = ref(false);
const {
  discardRequest,
  settleDiscard,
  credentials,
  securityActive,
  securityLock,
  session,
  current,
  latest,
  editing,
  draft,
  gender,
  loading,
  saving,
  failure,
  feedback,
  feedbackScope,
  pending,
  reconcile,
  errors,
  pollAttempts,
  pollDelays,
  permitted,
  recheckingOwner,
  busy,
  shown,
  ageText,
  showAge,
  publicLine,
  initials,
  fullName,
  memberSince,
  genderChoices,
  genderLabel,
  dirty,
  readProfile,
  startEditing,
  cancelEditing,
  save,
  toggleAge,
  acceptLatest,
  keepDraft,
  retryPending,
  Gender,
  birthLabel,
  avatarEnabled,
  comparisonRows,
} = useIdentityEditor();
const securityOpen = 'Сначала завершите или отмените действие в разделе «Вход и безопасность».';
const reconcileFirst = 'Сначала выберите версию данных в блоке сверки выше.';
/**
 * Причина, по которой правку данных сейчас не начать. Кнопка остаётся в порядке Tab с
 * `aria-disabled` и ведёт к причине; `disabled` — только пока идёт запрос.
 */
const editLock = computed(() =>
  reconcile.value ? reconcileFirst : securityActive.value ? securityOpen : '',
);
const ageLock = computed(() =>
  ageText.value === null
    ? 'Пока дата рождения не указана, показывать нечего.'
    : editing.value
      ? securityLock
      : reconcile.value
        ? reconcileFirst
        : securityActive.value
          ? securityOpen
          : '',
);
</script>

<template>
  <section
    class="account-section account-id"
    aria-labelledby="id-title"
    :inert="discardRequest !== null"
  >
    <div class="account-heading">
      <h1 id="id-title">MarketMesh ID</h1>
      <p class="lede">
        Один аккаунт для покупок, магазина и поддержки. Здесь ваши данные, вход и устройства, с
        которых вы заходили.
      </p>
    </div>
    <div v-if="pending || session.state.value.status === 'profilePending'" class="card state-card">
      <span class="loading-dot" aria-hidden="true"></span>
      <h2>Готовим ваш профиль</h2>
      <p role="status">
        Вход выполнен. Нам нужно немного времени, чтобы подготовить личное пространство.
      </p>
      <p v-if="pollAttempts >= pollDelays.length" class="field-help">
        Автоматическая проверка приостановлена. Можно проверить готовность вручную.
      </p>
      <button class="button secondary" :disabled="loading" @click="retryPending">
        {{ loading ? 'Проверяем…' : 'Проверить готовность' }}
      </button>
      <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
    </div>
    <div v-else-if="!permitted && !recheckingOwner" class="card state-card">
      <template v-if="['unknown', 'checking'].includes(session.state.value.status)"
        ><span class="loading-dot" aria-hidden="true"></span>
        <h2>Открываем ваш кабинет</h2>
        <p role="status">Проверяем сессию…</p></template
      >
      <template v-else
        ><h2>Личное начинается со входа</h2>
        <p>Войдите в аккаунт, чтобы посмотреть и изменить свои данные.</p>
        <RouterLink class="button primary" to="/login"
          >Перейти ко входу <span aria-hidden="true">↗</span></RouterLink
        ></template
      >
    </div>
    <div v-else-if="!current" class="card state-card" :aria-busy="loading">
      <p v-if="loading" role="status">Загружаем данные…</p>
      <template v-else
        ><h2>Данные пока недоступны</h2>
        <p v-if="failure" class="notice error" role="alert">{{ failure }}</p>
        <button class="button secondary" @click="readProfile()">
          Повторить загрузку
        </button></template
      >
    </div>
    <div v-else v-show="!recheckingOwner" class="id-content" :inert="recheckingOwner">
      <div class="card identity-card id-hero">
        <AvatarEditor v-if="avatarEnabled" :initials="initials" @active="avatarActive = $event" />
        <div v-else class="initials" aria-hidden="true">{{ initials }}</div>
        <div class="id-hero-text">
          <h2>{{ fullName }}</h2>
          <p>MarketMesh ID</p>
          <p v-if="memberSince">Вы с нами с {{ memberSince }}</p>
        </div>
        <div class="privacy-note">
          <p>Управлять аккаунтом можете только вы.</p>
        </div>
      </div>
      <nav class="id-section-links" aria-label="Разделы MarketMesh ID">
        <a href="#personal-title">Личные данные</a>
        <a href="#public-title">Публичные данные</a>
        <a href="#security">Вход и безопасность</a>
      </nav>
      <p v-if="failure && feedbackScope !== 'age'" class="notice error" role="alert">
        {{ failure }}
      </p>
      <div role="status">
        <p v-if="feedback && feedbackScope !== 'age'" class="notice success">{{ feedback }}</p>
      </div>
      <div
        v-if="reconcile"
        class="reconcile-panel"
        role="group"
        aria-labelledby="id-reconcile-title"
      >
        <h2 id="id-reconcile-title" tabindex="-1">Сверим с сохранёнными данными</h2>
        <button class="button secondary" :disabled="busy" @click="readProfile(true)">
          {{ loading ? 'Читаем…' : 'Перечитать актуальные данные' }}
        </button>
        <div v-if="latest" class="latest-profile">
          <p class="field-help">
            Сравните данные перед выбором. Новая запись выполнится только после сохранения.
          </p>
          <div class="id-comparison" role="region" aria-label="Сравнение версий" tabindex="0">
            <table>
              <caption>
                Ваши данные и сохранённая версия
              </caption>
              <thead>
                <tr>
                  <th scope="col">Поле</th>
                  <th scope="col">{{ editing ? 'Ваш черновик' : 'До сохранения' }}</th>
                  <th scope="col">Сохранено</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="row in comparisonRows"
                  :key="row.label"
                  :class="{ 'comparison-changed': row.changed }"
                >
                  <th scope="row">
                    {{ row.label }}<span v-if="row.changed" class="field-help">Отличается</span>
                  </th>
                  <td>{{ row.draft }}</td>
                  <td>{{ row.saved }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-if="editing" class="field-help">
            «Принять сохранённые данные» заменит ваш черновик. «Продолжить с черновиком» оставит
            введённые вами значения; видимость возраста будет взята из сохранённой версии.
          </p>
          <div class="button-row">
            <button class="button secondary" :disabled="busy" @click="acceptLatest">
              Принять сохранённые данные</button
            ><button v-if="editing" class="button text-button" :disabled="busy" @click="keepDraft">
              Продолжить с черновиком
            </button>
          </div>
        </div>
      </div>
      <section class="card profile-card" aria-labelledby="personal-title">
        <div class="card-heading">
          <div>
            <h2 id="personal-title" tabindex="-1">Личные данные</h2>
            <p class="subtle">
              Имя и город видны в отзывах. Почта, фамилия, телефон, дата рождения и пол другим
              покупателям не показываются.
            </p>
          </div>
          <button
            v-if="!editing"
            id="edit-identity"
            class="button secondary"
            :disabled="busy"
            :aria-disabled="editLock ? 'true' : undefined"
            :aria-describedby="editLock ? 'id-edit-locked' : undefined"
            @click="startEditing"
          >
            Изменить данные
          </button>
          <span v-else-if="dirty" class="draft-badge">Есть изменения</span>
        </div>
        <p v-if="editLock && !editing" id="id-edit-locked" class="field-help">{{ editLock }}</p>
        <dl v-if="!editing" class="id-rows">
          <div v-if="credentials" class="id-row">
            <dt>ПОЧТА</dt>
            <dd class="id-email">
              <span>{{ credentials.email }}</span
              ><span class="draft-badge">{{
                credentials.emailVerified ? 'Подтверждена' : 'Не подтверждена'
              }}</span>
            </dd>
          </div>
          <div class="id-row">
            <dt>ИМЯ</dt>
            <dd :class="{ subtle: !current.displayName }">
              {{ current.displayName || 'Не указано' }}
            </dd>
          </div>
          <div class="id-row">
            <dt>ФАМИЛИЯ</dt>
            <dd :class="{ subtle: !current.lastName }">{{ current.lastName || 'Не указана' }}</dd>
          </div>
          <div class="id-row">
            <dt>ДАТА РОЖДЕНИЯ</dt>
            <dd :class="{ subtle: !current.birthDate }">{{ birthLabel(current.birthDate) }}</dd>
          </div>
          <div class="id-row">
            <dt>ПОЛ</dt>
            <dd :class="{ subtle: current.gender === Gender.UNSPECIFIED }">
              {{ genderLabel(current.gender) }}
            </dd>
          </div>
          <div class="id-row">
            <dt>ТЕЛЕФОН</dt>
            <dd :class="{ subtle: !current.phone }">{{ current.phone || 'Не указан' }}</dd>
          </div>
          <div class="id-row">
            <dt>ГОРОД</dt>
            <dd :class="{ subtle: !current.city }">{{ current.city || 'Не указан' }}</dd>
          </div>
        </dl>
        <form v-else novalidate :aria-busy="saving" @submit.prevent="save">
          <div class="id-form-grid">
            <div class="field">
              <label for="id-first">Имя<span aria-hidden="true"> *</span></label>
              <input
                id="id-first"
                v-model="draft.displayName"
                type="text"
                autocomplete="given-name"
                :disabled="busy"
                :aria-invalid="Boolean(errors.displayName)"
                :aria-describedby="
                  errors.displayName ? 'id-first-help id-first-error' : 'id-first-help'
                "
              />
              <span id="id-first-help" class="field-help"
                >Имя видно в ваших отзывах и нужно мастеру.</span
              >
              <span v-if="errors.displayName" id="id-first-error" class="field-error">{{
                errors.displayName
              }}</span>
            </div>
            <div class="field">
              <label for="id-last">Фамилия</label>
              <input
                id="id-last"
                v-model="draft.lastName"
                type="text"
                autocomplete="family-name"
                :disabled="busy"
                :aria-invalid="Boolean(errors.lastName)"
                :aria-describedby="errors.lastName ? 'id-last-help id-last-error' : 'id-last-help'"
              />
              <span id="id-last-help" class="field-help"
                >Необязательно. В отзывах не показывается.</span
              >
              <span v-if="errors.lastName" id="id-last-error" class="field-error">{{
                errors.lastName
              }}</span>
            </div>
            <div class="field">
              <label for="id-city">Город проживания</label>
              <input
                id="id-city"
                v-model="draft.city"
                type="text"
                autocomplete="address-level2"
                :disabled="busy"
                :aria-invalid="Boolean(errors.city)"
                :aria-describedby="errors.city ? 'id-city-help id-city-error' : 'id-city-help'"
              />
              <span id="id-city-help" class="field-help"
                >Подставим в доставку и покажем в отзывах.</span
              >
              <span v-if="errors.city" id="id-city-error" class="field-error">{{
                errors.city
              }}</span>
            </div>
            <div class="field">
              <label for="id-birth">Дата рождения</label>
              <input
                id="id-birth"
                v-model="draft.birthDate"
                type="date"
                :disabled="busy"
                :aria-invalid="Boolean(errors.birthDate)"
                :aria-describedby="
                  errors.birthDate ? 'id-birth-help id-birth-error' : 'id-birth-help'
                "
              />
              <span id="id-birth-help" class="field-help"
                >Нужна для изделий с возрастным ограничением.</span
              >
              <span v-if="errors.birthDate" id="id-birth-error" class="field-error">{{
                errors.birthDate
              }}</span>
            </div>
            <div class="field">
              <label for="id-phone">Телефон</label>
              <input
                id="id-phone"
                v-model="draft.phone"
                type="tel"
                autocomplete="tel"
                :disabled="busy"
                :aria-invalid="Boolean(errors.phone)"
                :aria-describedby="errors.phone ? 'id-phone-help id-phone-error' : 'id-phone-help'"
              />
              <span id="id-phone-help" class="field-help">7–15 цифр, можно с пробелами и +.</span>
              <span v-if="errors.phone" id="id-phone-error" class="field-error">{{
                errors.phone
              }}</span>
            </div>
            <fieldset class="field theme-options" :aria-busy="saving">
              <legend>Пол</legend>
              <label v-for="choice in genderChoices" :key="choice.value" class="theme-choice"
                ><input
                  v-model="gender"
                  type="radio"
                  name="gender"
                  :value="choice.value"
                  :disabled="busy"
                  :aria-label="choice.label"
                  aria-describedby="id-gender-help"
                /><span>{{ choice.label }}</span></label
              >
              <span id="id-gender-help" class="field-help"
                >Необязательно. Другим покупателям не показывается.</span
              >
            </fieldset>
          </div>
          <p v-if="reconcile" id="id-save-reconcile" class="field-help">
            Сначала перечитайте сохранённые данные и выберите версию в блоке сверки выше.
          </p>
          <div class="form-footer">
            <span class="field-help">{{
              dirty ? 'Есть несохранённые изменения' : 'Проверьте данные перед сохранением'
            }}</span>
            <div class="button-row">
              <button
                class="button secondary"
                type="button"
                :disabled="busy"
                @click="cancelEditing"
              >
                Отменить</button
              ><button
                class="button primary"
                type="submit"
                :disabled="busy || reconcile || !dirty"
                :aria-describedby="reconcile ? 'id-save-reconcile' : undefined"
              >
                {{ saving ? 'Сохраняем…' : 'Сохранить изменения' }}
              </button>
            </div>
          </div>
        </form>
      </section>
      <section class="card profile-card" aria-labelledby="public-title">
        <div class="card-heading">
          <div>
            <h2 id="public-title" tabindex="-1">Публичные данные</h2>
            <p class="subtle">
              Это видят другие покупатели рядом с вашими отзывами. Показывается только то, что
              заполнено в личных данных.
            </p>
          </div>
        </div>
        <div class="public-layout">
          <dl class="id-rows public-rows">
            <div class="id-row">
              <dt>ИМЯ</dt>
              <dd :class="{ subtle: !shown.displayName.trim() }">
                {{ shown.displayName.trim() || 'Не заполнено — не показывается' }}
              </dd>
            </div>
            <div class="id-row">
              <dt>ГОРОД</dt>
              <dd :class="{ subtle: !shown.city.trim() }">
                {{ shown.city.trim() || 'Не заполнен — не показывается' }}
              </dd>
            </div>
            <div class="id-row">
              <dt>ВОЗРАСТ</dt>
              <dd :class="{ subtle: !(showAge && ageText !== null) }">
                {{ ageText === null ? 'Дата рождения не указана' : showAge ? ageText : 'Скрыт' }}
              </dd>
            </div>
          </dl>
          <div class="public-preview">
            <span class="field-help">Ваша подпись рядом с отзывом</span>
            <div class="review-preview">
              <p class="preview-name">{{ publicLine }}</p>
            </div>
            <p class="subtle preview-note">
              Фамилия, телефон и пол в отзывах не показываются никогда.
            </p>
          </div>
        </div>
        <label class="theme-choice age-choice">
          <input
            type="checkbox"
            :checked="showAge"
            :disabled="busy"
            :aria-disabled="ageLock ? 'true' : undefined"
            aria-describedby="age-switch-help"
            @click="ageLock && $event.preventDefault()"
            @change="toggleAge"
          />
          <span
            >Показывать возраст в отзывах
            <small id="age-switch-help">{{
              ageLock || `Сейчас другие покупатели видят: ${showAge ? ageText : 'без возраста'}.`
            }}</small></span
          >
        </label>
        <div class="age-feedback">
          <p v-if="feedbackScope === 'age' && !saving && failure" class="notice error" role="alert">
            {{ failure }}
          </p>
          <div role="status">
            <template v-if="feedbackScope === 'age'">
              <p v-if="saving">Сохраняем видимость возраста…</p>
              <p v-else-if="!failure && feedback" class="notice success">{{ feedback }}</p>
            </template>
          </div>
          <a v-if="feedbackScope === 'age' && reconcile" href="#id-reconcile-title"
            >Перейти к сверке данных</a
          >
        </div>
      </section>
    </div>
    <p v-if="recheckingOwner" role="status">Проверяем сессию…</p>
    <AccountSecurity
      :locked="
        editing
          ? securityLock
          : avatarActive
            ? 'Подождите, пока мы сохраним фото: выход из сеанса прервёт его.'
            : ''
      "
      @credentials="credentials = $event"
      @active="securityActive = $event"
    />
  </section>
  <Teleport to="body">
    <ConfirmDialog
      v-if="discardRequest"
      title="Удалить несохранённые изменения?"
      :confirm-label="
        discardRequest === 'leave' ? 'Удалить черновик и перейти' : 'Удалить черновик'
      "
      cancel-label="Продолжить редактирование"
      @confirm="settleDiscard(true)"
      @cancel="settleDiscard(false)"
      ><p>
        Сохранённые данные останутся прежними. Введённые изменения будут потеряны.
      </p></ConfirmDialog
    >
  </Teleport>
</template>
