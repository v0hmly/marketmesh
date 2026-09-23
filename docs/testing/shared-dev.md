# Проверка общей инфраструктуры MM-99

Проверено 23.09.2026 на локальном OrbStack. Сценарии и команды повторения —
в [руководстве dev](../../infra/dev/README.md). Изменение относится к текущему
локальному dev, не к топологии production.

## Состав и первоначальная установка

| Система | До MM-99 | После MM-99 |
| --- | --- | --- |
| PostgreSQL | 5: account primary/replica, Files primary/replica, Rybbit | 2: общий primary и синхронная replica |
| Redis | 2: Auth и Rybbit | 1: общий, отдельные ACL |
| ClickHouse | 1, часть Rybbit | 1, независимый общий сервис |
| NATS | 1 | 1, явный account MARKETMESH |

Выполнен разрешённый владельцем `task dev:reset`, затем `task dev:up` на пустых
volumes. Старые dev-данные не переносились. Taskboard и исходники сохранены;
другие Docker-проекты не затронуты. После запуска: 34 контейнера проекта,
30 работают, четыре init-job (`volume-init`, `signatures`, `provision`,
`observability-init`) завершились с кодом 0. Две реплики gateway-in сохранены.

## Результаты

- 18 unit tests bootstrap: выбор зависимостей, остановка генерации при неполном
  состоянии, ownership/symlink guards, проверка всех ресурсов до reset,
  сохранение секретов и замена только CA. Legacy-команды, которые скрыто вызывали
  `compose up`, отклоняются до Docker и до создания конфигурации.
- Compose config, Python AST, Node/shell syntax, layout/ссылки, diff whitespace
  прошли. Изменённые браузерные тесты прошли Prettier, ESLint и TypeScript.
- Образы аккаунта, Files, адаптированного Rybbit и browser собраны из исходников.
  Генерируемые API, конфигурации и отчёты не добавлены в Git.
- `dev:verify`: ровно один экземпляр каждой общей службы, sync standby
  `marketmesh_sync`, TLS verify-full, RW/RO Auth/User/Files, запрет межбазового
  доступа и записи RO. Проверены запреты соседних Redis namespaces (включая Lua)
  и административных команд, ограничение DDL/пользователей ClickHouse, SELECT
  query-user, mTLS NATS с разрешённым inbox и запретом чужих subjects/stream API.
- Chromium: десять прикладных/защитных сценариев — профиль/CAS/владельцы/сессии,
  адресная книга и лимиты, темы, идентификационные поля, прямые аватары Files,
  Mailpit verify/code/reset/security notices/change-email, аналитика с
  минимальными полями и отказом ingestion, отклонение чужого TLS-сертификата.
  Аналитика повторно проверена отдельно после передачи её dev-флага в runner.
  Pending-projection сценарий здесь пропускается: постоянный User consumer
  включён; сценарий остаётся в одноразовом `account:test`.
- `dev:verify -- seed/check` и `dev:browser -- --persistence seed/check`:
  новый аккаунт и настоящий аватар 96×96, Redis двух приложений и ClickHouse
  сохранились после `dev:down` → `dev:up`, затем после `dev:renew`.
  Вход, чтение профиля, состояние READY и загрузка байтов аватара прошли после
  обоих циклов. Rybbit продолжил читать прежнее событие через публичный API.
- Хеши общих паролей, Files database/credentials, mail HMAC, Rybbit env/admin
  до/после renew совпали; обе CA действительно изменились.
- После остановки трёх приложений Rybbit отдельный `dev:up -- clickhouse`
  не поднял их. Все пять общих container IDs остались прежними; ClickHouse
  прочитал сохранённый marker. `dev:up -- analytics-gateway` вернул аналитику.

## Остаточные риски и откат

Trivy 0.74.0 проверил закреплённый upstream Rybbit и производный образ MM-99.
Базовый digest: `sha256:5a824932c8b7b16364c8ee132e29d1d9d9574445e0d1f091e6364695e117bd66`.
Локальный image ID производного образа:
`sha256:18792f10a6166a03019d25f237a03a433a88bed5e5e73ecdbe2bafcab3a7ef65`.
В обоих отчётах одинаково: 6 Critical, 101 High, 107 Medium, 19 Low,
2 Unknown. Сравнение пар vulnerability/package/version не выявило добавленных
либо устранённых находок. MM-99 меняет конфигурацию четырёх мест upstream-кода,
не зависимости. Находки не подавляются; Rybbit не объявляется безопасным для
внешней эксплуатационной поставки. Исправление upstream CVE не входило в MM-99;
владелец ранее разрешил текущий dev без устранения доступных сейчас CVE.
Остаточные риски общих PostgreSQL/account и Files продолжают действовать по
[решению для dev](../security/files-dev-acceptance.md) с пересмотром до 22.10.2026.

Redis/ClickHouse используют plaintext только внутри Docker-сетей, базы не
публикуют порты на хост. Один Docker-хост не подтверждает независимость двух DC.
Полный CI с матрицей стендов не добавлен: GitHub Free использует прежний job,
эти интеграционные проверки выполняются локально.

Откат исходников — revert исходного PR. Возврат к прежней разделённой топологии
требует явного сброса dev и повторной установки: её volumes/ключи несовместимы
с новым общим кластером. Reset удаляет текущие данные; ни up, ни обычный revert
не выполняют его автоматически. Production и другие проекты не затрагиваются.
