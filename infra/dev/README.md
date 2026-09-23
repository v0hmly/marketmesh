# Локальный стенд одной командой

При запущенном OrbStack из корня репозитория:

```sh
task dev:up
```

Синоним — `task up`. Нужны Go, Python 3, OpenSSL, Docker Compose 2.24.4+ и Task
из корневого README. Frontend собирается внутри контейнера. Первый запуск
скачивает образы и антивирусные базы; для полного состава рекомендуется
выделить Docker не менее 12 ГБ RAM.

Все контейнеры входят в **один проект `marketmesh-dev`**, описанный корневым
[compose.yml](../../compose.yml). Он переиспользует определения контейнеров из
изолированных тестовых стендов через `extends`. Зависимости сервисов, ожидание
готовности и миграции заданы в Compose. Скрипт `local.py` подготавливает секреты,
сертификаты, OpenBao, bucket policies/CORS и локальный сайт Rybbit.

## Выбор сервисов

```sh
task dev:up                              # основной состав + все панели
task dev:up -- --profile core            # кабинет, Auth, User, Files и Mailpit
task dev:up -- --profile observability   # основной состав + Grafana Stack
task dev:up -- --profile analytics       # основной состав + Rybbit
task dev:up -- --profile infrastructure # только общие PG, Redis, NATS, ClickHouse
task dev:up -- clickhouse                # ClickHouse без Rybbit
task dev:up -- auth user                 # только Auth, User и их зависимости
task dev:up -- observability-gateway     # только Grafana Stack
task dev:up -- analytics-gateway         # только Rybbit
task dev:status
task dev:logs -- auth user
task dev:stop -- analytics-gateway analytics-client analytics-backend
task dev:down                            # остановка всего стенда, данные сохраняются
```

`core` означает сервисы без профиля; `full` включает также `analytics` и
`observability`. При указании имён сервисов запускаются они и их зависимости.
Подготовка файлового контура дополнительно запускает OpenBao и хранилища для
настройки ключей, политик и CORS. Уже запущенные сервисы не останавливаются от
того, что следующий `up` выбрал меньше сервисов. Останавливайте выбранные приложения через `dev:stop`; общие базы и очереди
эта команда останавливать не позволяет. Для остановки всего стенда — `dev:down`.

После успешного `task dev:up` доступны обычные команды Compose:

```sh
docker compose --env-file .cache/dev-stack/compose.env --profile full up -d
docker compose --env-file .cache/dev-stack/compose.env up -d auth user
docker compose --env-file .cache/dev-stack/compose.env --profile '*' down
```

Для первого запуска, обновления образов из исходников и истёкших сертификатов
используйте `task dev:up`: один `compose up` не выполняет host bootstrap.
`dev:config` готовит конфигурацию и проверяет её, но не инициализирует OpenBao
или Rybbit. Не публикуйте результат `compose config`: он содержит локальные секреты.

## Адреса

| Интерфейс | Адрес |
| --- | --- |
| Кабинет | https://localhost:18443 |
| Mailpit | http://localhost:18025 |
| Grafana | http://localhost:3000 |
| Rybbit | http://localhost:8310 |

Создайте аккаунт через `/register` и подтвердите почту письмом в Mailpit.
Пароль локального администратора Rybbit находится в
`.cache/dev-stack/rybbit/admin.json`. Письма остаются в локальном Mailpit.
Storefront подключается к Rybbit при включённом профиле аналитики.

Для HTTPS импортируйте публичные CA из
`.cache/dev-stack/account/browser/combined-ca.pem` в браузер или macOS «Связку
ключей». Приватные ключи импортировать не нужно. Системное доверие не меняется
автоматически; после перевыпуска CA обновите доверие.

## Данные и повторный запуск

Секреты и конфигурация находятся в `.cache/dev-stack`, данные — в именованных
volumes проекта. `down` не удаляет volumes. Для перехода со старой топологии сначала выполните **`task dev:reset`**, затем
`task dev:up`. Reset удалит прежние аккаунты, письма, файлы, аналитику, ключи и
volumes **только** проекта `marketmesh-dev` текущего checkout; миграции старых
данных нет. Повторный reset также удаляет новые данные. Taskboard и другие
Docker-проекты не затрагиваются. Проверяются owner-файл и labels каждого ресурса;
чужой worktree и символические ссылки отклоняются до удаления.

Старые отдельные проекты не удаляются автоматически. Для их ручной остановки
и очистки сохранены прежние `down`/`clean`/`reset`. `task account:up`,
`task analytics:up`, `task infra:up`, `task observability:up` запускают нужный
состав **этого же** `marketmesh-dev`. Прямой запуск старых persistent helpers
отклоняется с инструкцией. Изолированные `account:test` и `analytics:verify`
создают временный проект с уникальным именем и удаляют его после теста.

`up` автоматически обновляет истекающие сертификаты, сначала останавливая их
потребителей. `task dev:renew` принудительно обновляет сертификаты и запускает
стенд. Пароли БД, почтовый HMAC-ключ, письма, файлы и данные аккаунта сохраняются.
Прерванное обновление публичной PKI повторяется при следующем `up`.
Если конфигурация потеряна при сохранённых volumes, автоматическая генерация
других ключей запрещена: восстановите каталог состояния из своей копии.

Files использует короткие сертификаты и токены OpenBao (12 часов); после долгого
перерыва повторите `task dev:up`. Контейнер sandbox обрабатывает один документ
за запуск с обязательным перезапуском — это ограничение локального AV/CDR-контура.
Две delivery-зоны находятся на одном Docker-хосте и не являются независимыми DC.

В состав входят реализованные сервисы авторизации, кабинета, профиля, адресов,
настроек и аватаров. Остальные сервисы магазина добавляются по мере реализации.
Grafana Stack доступен для диагностики; запуск не означает, что весь трафик
приложений уже подключён к его collector. Ограничения контейнерных CVE — в
[приёмке Files](../../docs/security/files-dev-acceptance.md).

## Общие базы и очередь

| Система | Endpoint внутри сетей | Разделение |
| --- | --- | --- |
| PostgreSQL | `pg-primary:5432`, `pg-replica:5432` | БД `auth`, `user`, `files`, `analytics`; роли RW/RO каждого сервиса; отдельные `files_worker`, `rybbit` |
| Redis | `redis:6379` | ACL `auth` → `auth:session:access:*`; ACL `rybbit` → `session:*`, `sticky:*`, `rl:*`, `bot:*`, `feature-flags:*` |
| ClickHouse | `clickhouse:8123` | БД `analytics`; `rybbit` для записи/миграций, `rybbit_query` только SELECT `analytics.events` |
| NATS | `nats:4222` | mTLS, account `MARKETMESH`, stream `AUTH_REGISTRATION`, consumer `USER_PROFILES_V1`; точные publish/subscribe grants |

Ни один endpoint баз не опубликован на хост. Primary/replica общие для всех
потребителей; репликация синхронная `remote_apply`. Auth/User/Files используют
раздельные RW/RO DSN и TLS `verify-full`, Rybbit — TLS с проверкой имени/CA и
свою роль без cluster-admin. Роль Rybbit владеет только БД `analytics` для его
встроенных миграций. Межбазовые подключения приложений запрещены HBA и grants.

Redis сохраняет AOF (`everysec`) и не вытесняет сессии (`noeviction`); лимит
256 МБ. При исчерпании памяти запись отклоняется, при аварии возможно потерять
последнюю секунду. Разделение — ACL ключей/команд, не номера DB. Пароль `health`
разрешает только PING. Администраторские пароли находятся в `shared/credentials.json`
и не передаются приложениям. ClickHouse data volume принадлежит общей инфраструктуре:
его запуск и сохранность данных не зависят от включения Rybbit.

У закреплённого Rybbit 2.9 нет env-настроек username Redis/ClickHouse. Образ
`infra/dev/Dockerfile.rybbit` применяет проверяемый адаптер трёх мест кода:
username Redis, username ClickHouse writer, внешнее создание query-user.
Подключение query-user проверяется самим Rybbit; его права задаёт инфраструктура.
При изменении upstream-кода адаптер завершает сборку ошибкой. Vendor-код остаётся
в образе. Нельзя передавать username в URL ClickHouse: он переопределяет также
явный query-user и снимает разделение доступа.

Новый потребитель добавляется в `infra/dev/shared.py` (БД/роль/ACL), PG HBA либо
NATS account/subject permissions и корневой Compose. Его секреты генерируются
один раз в `.cache/dev-stack`, конфигурация повторяемая; доступ к соседнему
namespace проверяется отрицательным тестом. Новые постоянные экземпляры систем
ради отдельного приложения запрещены. При добавлении NATS-потребителя выделите
ему отдельную mTLS identity и точные subject/stream/consumer grants; для другого
trust-domain используйте другой account без неявных imports/exports.

В локальном dev Redis и ClickHouse доступны по plaintext только во внутренних
Docker-сетях; это не конфигурация production. Файловые quarantine/clean/delivery,
ключи KMS и DMZ остаются раздельными. Физическую независимость двух DC OrbStack
не моделирует.
