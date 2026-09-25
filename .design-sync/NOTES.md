# Заметки design-sync

- Дизайн-система — Vue + CSS (`frontend/packages/design-system`). Синхронизатор Claude Design
  рендерит только React, поэтому синхронизируется вариант «только токены и CSS-классы»
  (согласовано с пользователем при первой синхронизации, MM-123): `_ds_bundle.js` пуст,
  карточек компонентов нет, словарь классов описан в `conventions.md`.
- Сборка перед конвертером — `buildCmd` из `config.json`: генерирует `tokens.css`, кладёт
  пустой entry `node_modules/.ds-empty-entry.mjs` (PKG_DIR определяется по entry, а entry
  должен лежать внутри пакета) и через esbuild сворачивает `style.css` вместе с его
  `@import` в `node_modules/.ds-style.css`. Без этого `_ds_bundle.css` ссылается на
  `./tokens.css` и `./aliases.css`, которых нет в загрузке.
- Команда запуска драйвера из корня репозитория:
  `node .ds-sync/resync.mjs --config .design-sync/config.json --node-modules .ds-sync/node_modules --entry frontend/packages/design-system/node_modules/.ds-empty-entry.mjs --out ./ds-bundle [--remote .design-sync/.cache/remote-sync.json]`
- `guidelinesGlob` загружает `docs/design/design-system/README.md` и `components/*.md`
  в `guidelines/`.
- Классы из guidelines, которых нет в общем `style.css`: `.identity-card`, `.initials`,
  `.privacy-note`, `.reconcile-panel`, `.ctl`, `.draft-badge`, `.account-nav`,
  `.address-list`, `.account-heading`, `.compact`. Они живут в стилях приложения storefront
  и в дизайн-агент не попадают, поэтому в `conventions.md` не перечислены.
- `[FONT_MISSING] Inter` ожидаем: продукт сам не поставляет Inter (нет `@font-face`/woff в
  репозитории) и опирается на системный fallback `"Segoe UI", Arial`.

## Риски при повторной синхронизации

- Если в пакет добавят React-компоненты или сборку `dist/`, пересмотреть форму синхронизации.
- `conventions.md` перечисляет классы и токены вручную: после изменения `style.css` или
  `tokens.json` перепроверить каждое имя по `ds-bundle/_ds_bundle.css`.
- Визуально проверены только сборка и валидатор; превью-карточек нет, рендер не сравнивался.
