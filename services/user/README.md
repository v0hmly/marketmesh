# User

Сервис владеет профилем, настройками и пользовательскими атрибутами, которые не относятся к учётным данным входа. Он не имеет доступа к хранилищу Auth.

Граница ответственности определена в [ADR-0006](../../docs/adr/0006-auth-and-user-bounded-contexts.md).

## Минимальный runtime

При выключенном профильном feature flag сервис сохраняет минимальный runtime:

- `cmd/user` создаёт корневой context для `SIGINT` и `SIGTERM`, вызывает `internal/app.Run` и преобразует ошибку в exit code;
- `internal/app` типизированно загружает environment, вручную создаёт logger, telemetry и health, а безопасный server и middleware получает из `platform/httpserver`;
- `GET /livez` проверяет, что процесс жив, а `GET /readyz` отдельно отражает готовность принимать работу;
- HTTP server и telemetry останавливаются в обратном порядке с общим `SHUTDOWN_TIMEOUT`.

Обязательные environment variables:

| Variable | Назначение |
| --- | --- |
| `SERVICE_VERSION` | Версия сборки в logs и telemetry resource |
| `ENVIRONMENT` | Имя окружения, например `local` или `production` |
| `SERVICE_INSTANCE_ID` | Уникальный идентификатор экземпляра для telemetry |

Основные необязательные настройки:

| Variable | Значение по умолчанию |
| --- | --- |
| `HTTP_ADDRESS` | `127.0.0.1:8080` |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` |
| `HTTP_READ_TIMEOUT` / `HTTP_WRITE_TIMEOUT` | `15s` |
| `HTTP_IDLE_TIMEOUT` | `60s` |
| `HTTP_REQUEST_TIMEOUT` | `10s` |
| `HTTP_MAX_HEADER_BYTES` | `65536` |
| `HTTP_MAX_BODY_BYTES` | `1048576` |
| `HEALTH_CHECK_TIMEOUT` | `2s` |
| `SHUTDOWN_TIMEOUT` | `15s` |
| `LOG_LEVEL` / `LOG_CONSOLE` | `info` / `false` |
| `OTEL_ENDPOINT` | пусто, используется no-op pipeline |
| `OTEL_INSECURE` | `false` |
| `OTEL_TRACE_SAMPLE_RATIO` | `1` |
| `OTEL_AUTH_TOKEN` | пусто; значение всегда маскируется |

Локальный запуск без Collector:

```bash
SERVICE_VERSION=dev \
ENVIRONMENT=local \
SERVICE_INSTANCE_ID=user-local-1 \
go run ./cmd/user
```

## Профиль покупателя — MM-15

При `USER_PROFILE_ENABLED=true` сервис поднимает внутренний gRPC `user.v1.UserService`. Контракт находится в [user.proto](../../api/proto/user/v1/user.proto); Go, Connect и TypeScript генерируются через `task api:generate`.

- `GetMe` возвращает профиль вызывающего пользователя: `subject_id`, `display_name`, `bio`, `version` и время создания/изменения.
- `UpdateMe` полностью заменяет `display_name` и `bio` при совпадении `expected_version`. Пустое значение очищает поле. Отображаемое имя обрезается по краям и ограничено 80 Unicode-символами/320 байтами, текст «О себе» — 1000 символами/4000 байтами. Управляющие символы запрещены; в `bio` допустимы перевод строки и табуляция. Текст не является HTML и требует обычного экранирования при отображении.
- В запросах нет идентификатора целевого пользователя: владельца определяет только проверенное утверждение Auth. Успешное изменение увеличивает версию и возвращает записанное состояние; конфликт параллельного изменения возвращает `Aborted`, после чего клиент перечитывает профиль.
- Оба метода читают через RW primary, чтобы сохранять read-after-write. RO подключение указывает на настоящую recovery replica и проверяется при запуске/readiness; сценариев eventual consistency в этом срезе ещё нет.
- Get/Update не создают профиль. До обработки события регистрации отсутствие строки возвращает `NotFound` (`profile not ready`) с `google.rpc.ErrorInfo`: domain `marketmesh.user`, reason `PROFILE_NOT_READY`, без персональных metadata. Проверка сессии выполняется первой: недействующая сессия даёт `Unauthenticated` без этого detail. Клиент может показать подготовку профиля, сохраняя сессию. Consumer и восстановление описаны ниже; внешний HTTPS путь подключается через `USER_BROWSER_ENABLED` на обоих шлюзах (MM-62). Интерфейс кабинета реализуется следующими шагами MM-50.

`domain` содержит инварианты без protobuf/pgx, `application` — отдельные сценарии и порты хранилища, `adapter/out/postgres` — SQL, `adapter/out/authsession` — клиент проверки Auth, `adapter/in/internalgrpc` — преобразование транспорта и ошибок. User не импортирует реализацию Auth и не получает его DSN.

### Идентичность и приватность

Входящий TLS 1.3 требует клиентский сертификат с проверенной SPIFFE identity `spiffe://<trust-domain>/<environment>/gateway-out`. Политика разрешает этой роли только GetMe/UpdateMe. User использует сертификат роли `user` с обоими назначениями `serverAuth` и `clientAuth`; сервер Auth проверяется по CA, DNS server name и SPIFFE-роли `auth` того же окружения и trust domain.

Шлюз передаёт единственное внутреннее утверждение в бинарной gRPC metadata `marketmesh-session-assertion-bin`. Cookie, `Authorization`, дубли metadata и утверждения сверх 16 KiB отклоняются. User проверяет Ed25519-подпись по ограниченному набору открытых ключей Auth, issuer, audience `user`, тип и срок, затем получает свежую проверку сессии через `AuthInternalService.VerifyAssertion` и сверяет subject/session/expiry. Разрешения методов — `user:profile:read` и `user:profile:write`. Отзыв сессии проверяется при каждом вызове; успех не кэшируется. Недоступность Auth закрывает доступ. Причина синхронной зависимости зафиксирована в [ADR-0005](../../docs/adr/0005-user-session-and-identity-propagation.md).

Ошибки аутентификации дают `Unauthenticated`, недостаточные права — `PermissionDenied`, неверные поля — `InvalidArgument`, ошибки хранилища — безопасный `Unavailable`. Данные профиля, утверждения, DSN и ключи не попадают в сообщения ошибок и журналы. gRPC ответы помечены `cache-control: no-store`; типизированный bridge gateway-in устанавливает HTTP `Cache-Control: no-store` для обеих профильных route ID, включая ошибки разбора запроса. Настоящий внешний API использует отдельные browser route ID 102/103 и также устанавливает no-store; legacy 100/101 сохранены для FakeInternal E2E.

### Настройка runtime

По умолчанию `USER_PROFILE_ENABLED=false`: сохраняется описанный выше health-only runtime. Для включения нужны следующие настройки дополнительно к общим environment variables:

| Variable | Назначение / значение по умолчанию |
| --- | --- |
| `USER_PROFILE_ENABLED` | `true` включает профиль |
| `USER_GRPC_ADDRESS` | `127.0.0.1:9092` |
| `USER_TRUST_DOMAIN` | Обязательный trust domain SPIFFE |
| `USER_TLS_CERT_FILE` / `USER_TLS_KEY_FILE` | Абсолютные пути к сертификату и ключу User |
| `USER_TLS_CLIENT_CA_FILE` | Абсолютный путь к CA доверенных входящих workloads |
| `USER_AUTH_TARGET` | Обязательный gRPC endpoint Auth |
| `USER_AUTH_SERVER_NAME` | Обязательное DNS-имя в сертификате Auth |
| `USER_AUTH_CA_FILE` | Абсолютный путь к CA Auth |
| `USER_AUTH_ISSUER` | Ожидаемый издатель внутренних утверждений |
| `USER_AUTH_TIMEOUT` | `2s`, общий предел проверки Auth на запрос |
| `USER_ASSERTION_MAX_TTL` | `30s`, целое число секунд от `1s` до `5m` |
| `USER_GRPC_REQUEST_TIMEOUT` | `10s` |
| `POSTGRES_RW_DSN` / `POSTGRES_RO_DSN` | Секреты: primary и recovery replica одной базы User с разными LOGIN ролями |
| `POSTGRES_MAX_CONNS` | `10` на каждый pool; не более `100` |
| `POSTGRES_CONNECT_TIMEOUT` / `POSTGRES_QUERY_TIMEOUT` | `5s` / `3s` |

Readiness проверяет оба PostgreSQL endpoint, доступность столбцов `users.profiles` для обеих ролей и доступность проверяемого набора ключей Auth. Схема на реплике должна догнать миграцию перед включением экземпляра. При остановке сначала закрываются HTTP и gRPC listeners, затем Auth client, PostgreSQL pools и telemetry в пределах общего срока.

### Схема и развёртывание

[Миграция](migrations/000001_profiles.up.sql) создаёт `users.profiles` в отдельной базе User. Её применяет владелец схемы вне runtime; приложение не выполняет DDL. Начальная версия профиля — 1, opaque subject имеет ровно 16 байт и не может состоять целиком из нулей. UUID здесь не предполагается. Ограничения SQL дополняют предметную валидацию длины и версии.

До запуска настройте отдельные несуперпользовательские LOGIN роли: RW получает CONNECT к базе User, USAGE на схему `users`, SELECT/UPDATE на `users.profiles`; RO получает CONNECT, USAGE и SELECT. При включённом consumer RW дополнительно нужны INSERT на профили и SELECT/INSERT/UPDATE на inbox, как описано ниже. Runtime не нужны DELETE, CREATE, владение таблицами или членство в роли владельца. Для всех баз других сервисов, включая Auth, отзовите CONNECT у PUBLIC и выдавайте доступ только их владельцам/приложениям; у ролей User не должно быть такого разрешения. Используйте проверяемый TLS PostgreSQL в развёрнутых окружениях; отключённый TLS и простые пароли в testdata предназначены только для одноразовой внутренней Docker-сети.

Порядок включения: подготовить отдельную БД/реплику и роли → применить up-миграцию → дождаться replay на RO → выдать сертификаты и согласовать audience/scopes с Auth → включить `USER_PROFILE_ENABLED=true` → проверить `/readyz`. Для автоматического создания профиля дополнительно включается consumer ниже.

Безопасный откат runtime — выключить feature flag или вернуть предыдущую версию сервиса, сохранив таблицу и данные. Down-миграция удаляет профили и схему: она предназначена для одноразовых тестов либо отдельно согласованного удаления после остановки всех потребителей и сохранения резервной копии. Не запускайте её как обычный откат приложения.

### Проверки

Из корня workspace:

```bash
task verify
go test -race ./services/user/... ./services/gateway-in/internal/connectbridge/...
task user:integration
```

`task user:integration` собирает тестовый контейнер, запускает одноразовые PostgreSQL primary и streaming replica во внутренней Docker-сети без опубликованных портов и удаляет контейнеры/тома при завершении. Пакеты запускаются последовательно, поскольку проверка миграций пересоздаёт схему. Репозиторные тесты проверяют настоящий SQL, конкурентный CAS, права RO и цикл up/down. Тест composition root дополнительно использует настоящие TCP/mTLS gRPC соединения с контрактным Auth stub, подписанные утверждения, отдельные SQL роли, запрет CONNECT к чужой базе, отставание реплики, приватность ответов, деградацию readiness и graceful shutdown. Он не заменяет будущий E2E регистрации с настоящим Auth и браузером.

## Создание профиля из регистрации — MM-50, шаг 04

Опциональный consumer получает только `auth.account.registered.v1` из заранее подготовленного durable `USER_PROFILES_V1` в stream `AUTH_REGISTRATION`. User проверяет канонический protobuf, версию, производителя, размеры и ненулевые opaque ID. Auth implementation и Auth DSN в runtime User не используются.

Один SQL statement фиксирует `users.registration_inbox` и создаёт пустой профиль с тем же subject ID и версией 1. Inbox хранит event ID, subject, SHA-256 канонического payload и время приёма. Повтор с тем же ID и содержимым безопасен; повтор с изменённым содержимым отклоняется. Другой event ID того же subject также не перезаписывает профиль. Если профиль отсутствует при существующей корректной inbox-записи, replay восстанавливает его; существующие поля, версия и даты остаются прежними.

После успешного commit consumer вызывает подтверждение с ожиданием ответа брокера (`DoubleAck`). Авария между commit и ACK приводит к повтору, который проверяется inbox. Ошибки БД, неподдерживаемые или повреждённые сообщения не подтверждаются и получают delayed NAK. Они остаются в durable; `invalid` и `conflict` требуют реакции оператора. При исчерпании `max_ack_pending` обработка новых событий задерживается, пока причина не устранена. Runtime не выполняет terminal ACK, не удаляет ошибочные события и не меняет consumer configuration.

### Включение и права

Сначала применить [миграцию inbox](migrations/000002_registration_inbox.up.sql) владельцем схемы. Для RW роли включённого consumer добавить INSERT на `users.profiles` и SELECT/INSERT/UPDATE на `users.registration_inbox`. UPDATE inbox нужен для атомарного разрешения конкурентного повтора с проверкой содержимого. RO остаётся только читателем профилей. DDL, DELETE и доступ к базам Auth не выдаются.

Создать durable операционной ролью NATS: pull, `deliver_policy=all`, `ack_policy=explicit`, `filter_subject=auth.account.registered.v1`, `max_deliver=-1`, `max_ack_pending=64`, `ack_wait=30s`, без `deliver_subject`, дополнительных фильтров и `backoff`. Consumer проверяет эти ограничения; допустимый `ack_wait` строго больше двух operation timeouts плюс 1s и не больше 5m, `max_ack_pending` — 1..1024. Отклонённая конфигурация отражается `broker_error`, события остаются в брокере.

NATS workload User получает только:

- publish `$JS.API.CONSUMER.INFO.AUTH_REGISTRATION.USER_PROFILES_V1`;
- publish `$JS.API.CONSUMER.MSG.NEXT.AUTH_REGISTRATION.USER_PROFILES_V1`;
- publish `$JS.ACK.AUTH_REGISTRATION.USER_PROFILES_V1.>`;
- subscribe `_INBOX.>` для ответов pull/ACK.

Публикация доменных событий, чтение других consumers и создание/изменение/удаление stream/consumer запрещены. Настройка для стандартного JetStream без domain префикса; добавление NATS domain требует отдельного согласования subjects/ACL. TLS 1.3 проверяет CA и DNS брокера, а клиентский сертификат должен содержать SPIFFE User текущего trust domain и окружения, действующий срок и `clientAuth`. Broker `verify_and_map` должен сопоставлять его отдельному principal User; test fixture использует отдельный email SAN для mapping.

| Variable | Назначение / значение по умолчанию |
| --- | --- |
| `USER_REGISTRATION_CONSUME_ENABLED` | `false`; для включения также требуется `USER_PROFILE_ENABLED=true` |
| `USER_NATS_URL` | Обязательный один `tls://host:port`, без credentials, query и path |
| `USER_NATS_SERVER_NAME` | Проверяемое DNS-имя NATS |
| `USER_NATS_TLS_CERT_FILE` / `USER_NATS_TLS_KEY_FILE` / `USER_NATS_TLS_CA_FILE` | Обязательные абсолютные пути |
| `USER_EVENTS_CONNECT_TIMEOUT` | `2s`; общий deadline DNS/TCP/INFO/TLS и bind |
| `USER_EVENTS_OPERATION_TIMEOUT` | `5s`, не больше 30s; не меньше connect timeout и PostgreSQL query timeout |
| `USER_EVENTS_FETCH_TIMEOUT` | `1s`, от 10ms до 5s |
| `USER_EVENTS_RETRY_DELAY` | `1s`, от 1s до 1m |

После миграции и выдачи прав включить capture/publisher Auth и consumer User. Отсутствие таблицы или обязательных прав inbox ухудшает User readiness. Недоступность NATS не закрывает уже существующие профили: брокер не является критической readiness-зависимостью, consumer повторяет соединение. При остановке сначала закрываются пользовательские listeners, затем consumer, Auth client, БД и telemetry. Отмена незавершённого применения оставляет событие для повторной доставки.

### Наблюдение и сверка

`marketmesh.user.registration.delivery` — счётчик с единственным label `outcome`: `applied`, `duplicate`, `invalid`, `conflict`, `store_error`, `broker_error`, `ack_error`. `pending` и `ack_pending` в том же namespace — gauges состояния durable; `backlog_observed_at` содержит Unix-время последнего успешного чтения этих показателей. Проверяйте свежесть: при сбое NATS gauges могут устареть. `delivery_age` — histogram возраста успешно применяемого события в секундах, включая повторы; это не возраст старейшего сообщения очереди. Идентификаторы и персональные данные в labels отсутствуют.

Операционные проверки выполняются раздельно в каждом сервисе. Auth проверяет отсутствующие outbox-факты своей командой, User проверяет целостность собственной проекции:

```sql
SELECT count(*) AS inbox_subjects_without_profile
FROM (SELECT DISTINCT subject_id FROM users.registration_inbox) i
LEFT JOIN users.profiles p USING(subject_id)
WHERE p.subject_id IS NULL;
```

Нулевой результат подтверждает целостность полученных фактов, но не доказывает получение всех аккаунтов Auth. Для сверки ранее зарегистрированных аккаунтов используйте [Auth backfill](../auth/README.md#восстановление-регистраций--mm-50-шаг-04): полный проход missing-only, при необходимости явный replay опубликованных событий с учётом duplicate window, затем проверьте отсутствие pending outbox и свежие `pending=0`, `ack_pending=0`, отсутствие `invalid/conflict`, и повторите локальную проверку User. Простое равенство количества аккаунтов и профилей не считается доказательством соответствия.

Срок хранения inbox должен покрывать весь период replay/backfill; автоматической очистки в этом шаге нет. Откат consumer — выключить его флаг или вернуть предыдущий runtime, сохранив обе таблицы и durable. Capture Auth можно оставить включённым. Down-миграция удаляет только inbox и теряет историю дедупликации; её нельзя использовать как обычный откат приложения.

### Сквозная проверка

```bash
task user:registration:integration
```

Стенд запускает реальные Auth registration/publisher и backfill CLI, User runtime, отдельные PostgreSQL primary/replica и NATS с mTLS/ACL. Проверяются доставка, сохранение изменений при повторе, отказ БД/брокера, backfill, состояние подготовки профиля и отмена. Только неизменённая RPC-граница ключей/проверки сессии Auth использует явно обозначенный контрактный stub; полный browser/login путь относится к следующим шагам MM-50. Runtime и CLI собираются под Linux. Файлы PKI создаются в именованном томе; host ports и bind mounts отсутствуют, ресурсы удаляются после проверки. `REGISTRATION_TEST_IMAGE` позволяет подставить локальный проверенный offline test image.

Проверка имеет отдельный build tag `registrationintegration` вместе с `integration`, чтобы обычный `task user:integration` не требовал NATS или Auth binary. Протокольные основания: [NATS consumer configuration](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/consumers.md).
