# Frontend

Vue и pnpm workspace с двумя самостоятельными приложениями по
[ADR-0016](../docs/adr/0016-frontend-application-boundaries.md):

- [storefront](apps/storefront/README.md): auth, кабинет покупателя и seller;
- [staff](apps/staff/README.md): корпоративный портал, отдельный origin и SSO;
- `packages/browser-client`: технический HTTPS/Connect-транспорт и восстановление чанков;
- `packages/design-system`: общие стили, знак и диалог; значения из канонического
  `docs/design/design-system/tokens.json`;
- `gen`: игнорируемые protobuf-клиенты.

Из корня чистого checkout:

```sh
pnpm --dir frontend install --frozen-lockfile
task storefront:verify
task staff:verify
task frontend:verify
```

Целевые команды проверяют приложение и его общие зависимости; общая проверка
сохраняет интеграционный охват. Генерация API и CSS выполняется до сборки, отдельный
коммит результатов не нужен. Перед Vite dev выполните `task generate`.
Полный локальный стенд — `task dev:up`.

ESLint проверяет приложения **и пакеты**, включая именованные реэкспорты и
динамические импорты. `node --test frontend/tools/architecture/*.test.mjs`
проверяет отрицательные примеры настоящим ESLint. `src/testing` разрешён только
тестам, runtime импортировать его не может.

Размеры production-артефактов, условия измерения и browser-проверки —
[в отчёте MM-87](../docs/frontend-bundles.md). Никакой runtime Module Federation
или отдельной версии приложения не вводится.
