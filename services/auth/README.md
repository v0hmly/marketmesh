# Auth

Auth владеет учётными данными, непрозрачными идентификаторами субъектов и
состоянием браузерных сессий. Граница ответственности определена в
[ADR-0006](../../docs/adr/0006-auth-and-user-bounded-contexts.md), а направление
зависимостей — в [ADR-0011](../../docs/adr/0011-go-service-hexagonal-architecture.md).

## Границы и безопасность

- `internal/domain/credential` содержит чистые типы `Identifier`, `Password`,
  `SubjectID`, `PasswordDigest` и их инварианты.
- `internal/application/register`, `internal/application/login` и
  `internal/application/session` используют порты возле потребителя; они не
  импортируют protobuf, pgx, Redis, logger или OpenTelemetry.
- `internal/adapter/in/connectrpc` отображает DTO и возвращает стабильные
  публичные ошибки. `internal/adapter/in/internalgrpc` обслуживает только
  private assertion API.
- `internal/adapter/out` содержит bcrypt, PostgreSQL, Redis и управление
  ключами session assertions.
- `internal/app` вручную собирает зависимости, подключает runtime, telemetry и
  graceful shutdown.

Идентификатор нормализуется без сохранения исходного значения. Пароль должен
содержать 8–64 Unicode code points и занимать не более 72 байт UTF-8; пробелы,
регистр и Unicode-представление не изменяются. Кириллица и эмодзи расходуют
байтовый лимит быстрее ASCII. Пароль копируется на доменной границе и очищается
после использования. Backend и frontend применяют одинаковые границы при
регистрации и входе, без обрезания ввода. Нулевой символ U+0000 запрещён:
он может сделать разные пароли эквивалентными при расширении ключа bcrypt.

В БД сохраняется bcrypt-строка с версией, стоимостью, случайной солью и хэшем.
Используется `golang.org/x/crypto/bcrypt`. Неизвестный идентификатор и неверный
пароль дают одинаковый `Unauthenticated: invalid credentials`; для неизвестного
идентификатора выполняется bcrypt-проверка случайного process-local dummy digest.
Повторная регистрация существующего identifier не отличается от успешной
регистрации ни телом, ни кодом ответа и также проходит хэширование. При успешном
входе меньшая стоимость bcrypt обновляется compare-and-swap запросом; большая
стоимость не понижается. Перед вычислением проверяются формат и верхний предел
стоимости сохранённого хэша, а также ограничение bcrypt в 72 байта.

Минимум 8 символов выбран как продуктовый компромисс. Для входа без MFA он ниже
[рекомендации NIST в 15 символов](https://pages.nist.gov/800-63-4/sp800-63b.html#passwordver).
Ограничение длины само по себе не заменяет защиту от перебора.

Сервис поддерживает только bcrypt. По согласованному решению MM-72 совместимость
с прежними хэшами не сохраняется: действующего стенда и данных для миграции нет.
Смешанное развёртывание со старой версией Auth не поддерживается. Если сохранён
старый локальный набор учётных записей, его нужно отдельно пересоздать; сервис
не удаляет данные автоматически. Откат на старую версию также требует её
совместимого набора данных, поскольку bcrypt-хэши она не проверяет.

Сырые пароли, cookie-токены, access/refresh tokens, digest и ключевой материал не
попадают в логи, трассировки, метрики или клиентские ошибки. Токены сессии
существуют только в памяти доверенного Auth transport adapter; хранятся только их
SHA-256 digest. Аудит входа содержит только конечные категории `outcome` и
`reason`, а категории метрик имеют ограниченную кардинальность.

## Публичные API и браузерные сессии

Контракт находится в `api/proto/auth/v1/auth.proto` и генерируется для Go и
TypeScript. `RegisterCredentials` сохраняет credential без сигнала о наличии
identifier. `Login` проверяет credential и при включённых сессиях создаёт
session family. Публичный lifecycle состоит из cookie-only методов:

- `Login` принимает identifier/password; токены не возвращаются в body.
- `RefreshSession` принимает только refresh cookie и атомарно ротирует семью.
- `Logout` отзывает сессию по access cookie.
- `LogoutAll` отзывает все сессии субъекта по access cookie.

Ответы устанавливают только `__Host-mm-access` и `__Host-mm-refresh`. Оба cookie
имеют `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/` и не имеют `Domain`;
это удовлетворяет ограничениям `__Host-`. Refresh и access не принимаются в
теле, URL или пользовательских заголовках.

Для каждого cookie-only метода handler требует ровно один `Origin`, совпадающий
с точным значением из `AUTH_ALLOWED_ORIGINS` (только HTTPS origin без path,
query, fragment или userinfo), и отклоняет `Sec-Fetch-Site: cross-site`.
Это allowlist и CSRF защита на границе Auth; отсутствие Origin, `null`, дубликаты
cookie и слишком большой Cookie header отклоняются.

В PostgreSQL canonical state содержит текущий refresh digest, версию, сроки и
отзыв сессии. `consumed_refresh_digests` хранит использованные digest до конца
retention семьи. Ротация, обнаружение повторного refresh и отзыв семьи выполняются
в одной транзакции: повторное использование отзывает семью и создаёт outbox
event, неизвестный digest семью не отзывает. Redis хранит в изолированной роли
`RoleAuth` короткоживущий access digest, expiry и версию; проверка всегда также
сверяет canonical PostgreSQL state.

`auth.session_revocation_outbox` — транзакционный журнал для будущего worker,
который сможет доставлять события для WS/SSE. Доставка пока не реализована;
публикация в NATS не обещается и этим кодом не выполняется.

## Внутренний assertion API

`AuthInternalService` доступен только на отдельном workload-authenticated gRPC
listener с mTLS. В production используются TLS 1.3, certificate Auth с
идентичностью `auth`, проверка client certificate через доверенный CA и
workload policy. Browser/public HTTP к этому listener не маршрутизируется.

`ExchangeSession` разрешён только роли `gateway-out`: gateway передаёт cookie
неизменённым, Auth проверяет online access state и выпускает короткий assertion
для заранее настроенной audience. `VerifyAssertion` выводит audience из
аутентифицированной роли caller, проверяет подпись, issuer, TTL и session ID, а
затем снова проверяет online canonical session state и Redis version. Поэтому
отзыв применяется немедленно при следующем online `VerifyAssertion`; одна только
offline-проверка подписи не даёт немедленного отзыва.

Offline assertions допустимы только для bounded TTL и срока жизни сессии:
`ExchangeSession` ограничивает assertion остатком access/absolute lifetime, а
конфигурация ограничивает TTL максимумом 5 минут. Сервис-потребитель обязан
проверять audience, expiry и scopes согласно своему workload policy.

`GetSigningKeys` возвращает только публичные Ed25519 JWK (`OKP`/`Ed25519`, без
private key), включая `verify_until`; caller не выбирает URL или источник ключей.
Файл ключей перечитывается и валидируется как единый снимок. Для ротации сначала
готовится полный файл и затем выполняется атомарная замена. Должен существовать
ровно один активный signer, окна подписи не пересекаются, а `verify_until`
включает bounded overlap не меньше максимального assertion TTL. Подробная схема,
формат, сроки и правила защиты `kid` описаны в
[README keyring](internal/adapter/out/sessionkeys/README.md).

## PostgreSQL и миграции

Миграции применяет отдельный доверенный migration job; Auth не применяет их при
старте. Порядок обязателен: сначала `000001_credentials`, затем
`000002_sessions`. Сессии ссылаются на `auth.credentials(subject_id)`.

Все security-sensitive чтения выполняются через RW executor, чтобы replica lag не
влиял на решение аутентификации. Запросы статические и параметризованные.
Откат выполняется поэтапно: сначала `AUTH_SESSIONS_ENABLED=false`, затем
останавливается новый session traffic, и только после retention/операционного
решения удаляются или откатываются session data. Нельзя сохранить живые сессии
при downgrade миграции: rollback session schema требует их инвалидировать.

## Конфигурация

При `AUTH_SESSIONS_ENABLED=false` (значение по умолчанию) session resources,
Redis и internal gRPC listener не создаются; это staged rollout flag. При
включении обязательны `AUTH_SESSION_ISSUER`, `AUTH_SESSION_KEYS_FILE`,
`AUTH_TRUST_DOMAIN`, `AUTH_INTERNAL_TLS_CERT_FILE`,
`AUTH_INTERNAL_TLS_KEY_FILE`, `AUTH_INTERNAL_CLIENT_CA_FILE`,
`AUTH_ALLOWED_ORIGINS` и `AUTH_SESSION_AUDIENCES` (JSON map audience → scopes).
Пути ключа и TLS/CA должны быть абсолютными; origins — точный HTTPS allowlist.

| Переменная | По умолчанию |
| --- | --- |
| `HTTP_ADDRESS` | `127.0.0.1:8081` |
| `HTTP_MAX_BODY_BYTES` | `16384` |
| `HTTP_REQUEST_TIMEOUT` | `10s` |
| `AUTH_INTERNAL_ADDRESS` | `127.0.0.1:9091` |
| `AUTH_ACCESS_TTL` | `10m` |
| `AUTH_REFRESH_IDLE_TTL` | `168h` (7 дней) |
| `AUTH_SESSION_ABSOLUTE_TTL` | `720h` (30 дней) |
| `AUTH_ASSERTION_TTL` | `30s` |
| `POSTGRES_MAX_CONNS` | `10` |
| `POSTGRES_QUERY_TIMEOUT` | `3s` |
| `BCRYPT_COST` | `12`; допустимо `10`–`14` |

Также требуются общие `SERVICE_VERSION`, `ENVIRONMENT`, `SERVICE_INSTANCE_ID`,
`POSTGRES_RW_DSN` и `POSTGRES_RO_DSN`. Для включённых сессий обязательны
`AUTH_REDIS_ADDRESS` и `AUTH_REDIS_PASSWORD`; по умолчанию нужен проверенный
Redis TLS (`AUTH_REDIS_TLS_SERVER_NAME`, опционально `AUTH_REDIS_CA_FILE`).
`AUTH_REDIS_CONNECT_TIMEOUT` ограничивает установление соединения вместе с TLS
и начальной Redis-командой: по умолчанию `1s`, допустимо до `10s`. Локальный
DC E2E использует `5s`: проверенный TLS bootstrap между VM занимает около `1.3s`.
Дедлайны проверки сессии и проверки отзыва этим параметром не изменяются.
Plaintext Redis разрешён только с явным `AUTH_REDIS_PLAINTEXT_REASON`, не в
production и не вместе с TLS-настройками.

## Проверка

Команды для проверки изменения:

```bash
go test ./services/auth/...
go test -race ./services/auth/...
go vet ./services/auth/...
bash services/auth/internal/adapter/out/postgres/testdata/integration.sh
task auth:sessions:integration
(cd services/auth && govulncheck ./...)
task verify
```

`auth:sessions:integration` проверяет lifecycle с настоящими PostgreSQL и Redis
в одноразовом изолированном окружении. Доставка событий в WS/SSE или NATS
остаётся задачей будущего worker.

## Регистрация и JetStream — MM-50, шаг 03

Регистрация может сохранять учётную запись и событие `auth.account.registered.v1` одним SQL statement с двумя связанными INSERT CTE. Нарушение ограничения outbox откатывает и credential; конкурентные регистрации одного identifier сохраняют ровно одну пару. Повторная регистрация сохраняет прежний внешний ответ без признака существования аккаунта. PostgreSQL не вызывается из доменной модели, а прикладной порт `RegistrationWriter.CreateRegistration` явно задаёт атомарную границу.

Событие описано в [events.proto](../../api/proto/auth/v1/events.proto). Его непрозрачный `event_id` имеет 16 байт и не меняется при повторной публикации. `subject_id` — единственный предметный payload и идентификатор агрегата. Envelope фиксирует тип, версию 1, производителя `auth`, время с микросекундной точностью и необязательные trace/causation ID; входящий tracing baggage не переносится. Identifier/email, password digest, cookie, токены и профиль в событие не входят. Канонический protobuf payload сохраняется в outbox целиком и не пересобирается издателем.

### Доставка

Фоновый издатель получает по одной записи через `FOR UPDATE SKIP LOCKED`, устанавливая случайный lease token и срок. Транзакция БД завершается до обращения к NATS. Издатель отправляет сохранённый payload в точный subject `auth.account.registered.v1` с `Nats-Msg-Id=hex(event_id)` и ожидаемым stream `AUTH_REGISTRATION`. `published_at` выставляется только после положительного синхронного PubAck с правильным stream и ненулевой sequence, причём только владелец ещё действующего lease может изменить запись.

Ошибка публикации планирует ограниченную экспоненциальную задержку. Авария между PubAck и записью `published_at` оставляет событие для повторной доставки после истечения lease. Это доставка «как минимум один раз»; дедупликация JetStream уменьшает повторы в своём временном окне, а будущий User consumer должен сохранять event ID в своём inbox в одной транзакции с созданием профиля. Обработка события и backfill существующих аккаунтов относятся к шагу 04 MM-50.

NATS не является критической readiness-зависимостью Auth: его недоступность увеличивает очередь и задержку создания профиля, но не откатывает регистрацию. Успех в API означает сохранённые credential и outbox, а не готовый User-профиль. Схема и права outbox в PostgreSQL проверяются через readiness. Выключение runtime останавливает HTTP, затем издатель и сессии, затем БД/telemetry; неопределённый результат публикации остаётся для повторной попытки.

### Настройки и поэтапное включение

| Variable | Назначение / значение по умолчанию |
| --- | --- |
| `AUTH_REGISTRATION_EVENTS_ENABLED` | `false`; `true` включает атомарное сохранение событий |
| `AUTH_REGISTRATION_PUBLISH_ENABLED` | `false`; требует включённого сохранения; `true` запускает издателя |
| `AUTH_NATS_URL` | Один обязательный `tls://host:port` без credentials, query и path при включённом издателе |
| `AUTH_NATS_SERVER_NAME` | Проверяемое DNS-имя брокера |
| `AUTH_EVENTS_TRUST_DOMAIN` | Trust domain Auth workload |
| `AUTH_NATS_TLS_CERT_FILE` / `AUTH_NATS_TLS_KEY_FILE` | Абсолютные пути к клиентскому сертификату Auth и ключу |
| `AUTH_NATS_TLS_CA_FILE` | Абсолютный путь к доверенному CA брокера |
| `AUTH_EVENTS_CONNECT_TIMEOUT` / `AUTH_EVENTS_PUBLISH_TIMEOUT` | `2s` / `5s`; connect не больше publish, publish не больше `30s` |
| `AUTH_EVENTS_POLL_INTERVAL` | `1s`, от `10ms` до `1m` |
| `AUTH_EVENTS_BATCH_SIZE` | `32`, не больше `256` за один проход |
| `AUTH_EVENTS_LEASE_DURATION` | `30s`, не больше `5m`; строго больше publish timeout + два `POSTGRES_QUERY_TIMEOUT` + `1s` |
| `AUTH_EVENTS_RETRY_INITIAL` / `AUTH_EVENTS_RETRY_MAX` | `1s` / `1m`; начальная задержка от `1ms`, максимум не больше `24h` |

При обоих выключенных флагах сохраняется прежняя регистрация без outbox. Такие аккаунты, включая созданные до включения, потребуют backfill. После включения сохранения можно отдельно приостановить издателя, оставив события на диске.

1. Владелец схемы применяет [000003_registration_outbox.up.sql](migrations/000003_registration_outbox.up.sql) после credentials/sessions migrations. Это расширение схемы; runtime DDL не выполняет. Auth RW получает SELECT/INSERT/UPDATE на `auth.registration_outbox`; RO не используется издателем. Старый runtime продолжает работать при установленной новой таблице.
2. Включается `AUTH_REGISTRATION_EVENTS_ENABLED=true` и проверяются readiness/атомарная регистрация. У аккаунтов, зарегистрированных выключенной версией во время последовательного развёртывания, событий ещё нет — их покрывает будущий backfill.
3. Администратор брокера создаёт stream `AUTH_REGISTRATION` с единственным subject `auth.account.registered.v1`, file storage и Limits retention. Для dev/test достаточно одной реплики; в рабочем кластере используйте три. Ограничьте общий размер согласно ёмкости окружения; `DiscardNew` при заполнении сохраняет неотправленные события в outbox. Срок хранения должен покрывать оговорённое окно восстановления; для начального развёртывания не включайте автоматическое удаление по возрасту до готовности потребителя. Duplicate window, например 10 минут, не заменяет долговременный inbox потребителя.
4. NATS должен требовать TLS 1.3 и клиентский сертификат. Для `verify_and_map` сопоставьте выделенную identity сертификата с пользователем, которому разрешены только публикация `auth.account.registered.v1` и подписка на `_INBOX.>` для PubAck. Права `$JS.API.>` и административные credentials приложению не выдаются; provisioning stream выполняется отдельно. Сертификат приложения также содержит `spiffe://<trust-domain>/<environment>/auth` и назначение clientAuth. Стенд использует отдельный email SAN для NATS user; production PKI должна выдавать его только уполномоченному Auth workload.
5. Выдаются настройки TLS/NATS и включается `AUTH_REGISTRATION_PUBLISH_ENABLED=true`. Проверяется уменьшение очереди и получение PubAck. Сам runtime не создаёт stream/consumer и не публикует события отзыва сессий из `auth.session_revocation_outbox`.

### Наблюдение и восстановление

Издатель экспортирует `marketmesh.auth.registration.pending`, `marketmesh.auth.registration.oldest_pending_age` (секунды) и счётчик `marketmesh.auth.registration.delivery` с ограниченными исходами `published`, `publish_error`, `store_error`, `lease_lost`. В labels нет идентификаторов пользователей или событий. При отключённом издателе состояние очереди проверяется SQL; метрики остановленного издателя не являются текущим состоянием.

```sql
SELECT count(*) AS pending, min(occurred_at) AS oldest_pending
FROM auth.registration_outbox WHERE published_at IS NULL;
```

Рост возраста и повторов требует проверки TLS/ACL, существования stream, места в JetStream и доступности PostgreSQL. Повреждённый payload не подтверждается молча: остаётся в очереди и увеличивает показатели ошибок; его исправление требует проверки первичного факта оператором. Очистка подтверждённых событий и сроки хранения требуют отдельной эксплуатационной политики; этот runtime не удаляет outbox автоматически и не обещает replay уже удалённых событий из брокера.

Откат: сначала выключить издателя, сохранив capture, либо откатить runtime с сохранённой таблицей. Выключение capture возвращает регистрацию без событий и создаёт необходимость backfill. Down-миграция удаляет очередь, включая ещё не опубликованные события: это не штатный откат приложения, её применение требует отдельного согласования после остановки производителей/издателей и резервной копии.

### Проверки регистрации

Из корня репозитория:

```bash
task verify
task auth:registration:integration
```

Одноразовый integration стенд включает PostgreSQL primary/recovery replica и настоящий NATS JetStream с mTLS и ограниченными ACL. Файлы сертификатов создаются внутри отдельного именованного тома; host bind mounts и опубликованные порты отсутствуют. Тесты выполняются последовательно, ресурсы удаляются при завершении. Проверяются конкурентная регистрация, атомарный rollback, истечение/перехват lease, повтор после PubAck без mark, дедупликация, запрет чужих subjects и admin API, capture-only → публикация, сбой/восстановление NATS, readiness и graceful shutdown. Переменная `REGISTRATION_TEST_IMAGE` позволяет использовать заранее собранный локальный проверенный тестовый образ в окружении без доступа Docker builder к Go proxy.

Протокольные основания: [JetStream publishing](https://docs.nats.io/learn/jetstream/publishing), [NATS mTLS и mapping сертификата](https://docs.nats.io/learn/security/encryption).

## Восстановление регистраций — MM-50, шаг 04

`cmd/auth-registration-backfill` — отдельная операционная команда владельца Auth. Она сверяет только opaque subject ID credentials с Auth outbox, не читает identifier/password digest и не имеет подключения к базе User. Изменения доходят до User через обычные outbox → JetStream → transactional inbox. Команда не запускается автоматически при старте сервиса.

Перед первым проходом включите `AUTH_REGISTRATION_EVENTS_ENABLED=true`: иначе параллельная регистрация со случайным ID раньше текущего cursor может быть пропущена. Подготовьте consumer User и издателя Auth. Передайте DSN отдельной роли через `MARKETMESH_AUTH_POSTGRES_DSN` средствами управления секретами. Ей нужны SELECT(subject_id) на `auth.credentials`, SELECT на `auth.registration_outbox`; для apply дополнительно INSERT на outbox и UPDATE(published_at,next_attempt_at). Права на credential identifier/digest, DDL, DELETE, владение таблицей или User DB не нужны. Используйте PostgreSQL с проверяемым TLS в развёрнутых окружениях.

Из корня Go workspace:

```bash
go run ./services/auth/cmd/auth-registration-backfill --limit=100
go run ./services/auth/cmd/auth-registration-backfill --limit=100 --apply
```

По умолчанию выполняется dry-run без мутаций. Один вызов обрабатывает одну страницу (1..1000 аккаунтов), выводит JSON с `Scanned`, `Missing`, `Published`, `Inserted`, `Requeued`, `NextCursor`, `Done`. Передайте `--after=<NextCursor>` следующему вызову; при `Done=false` повторяйте до завершения. При ошибке cursor относится к последней полностью обработанной строке; неопределённый результат записи безопасно перепроверяется при повторе. JSON не содержит credentials или payload, но cursor — opaque идентификатор, поэтому храните операционный вывод с ограниченным доступом.

Apply создаёт отсутствующее outbox-событие условной вставкой при существующем Auth subject. Конкурирующие запуски сохраняют одну запись. Для исторического аккаунта время нового восстановленного события соответствует моменту его создания backfill. Уже существующие события остаются неизменными. Полный повтор missing-only прохода безопасен.

Для восстановления после потери сообщений из stream retention предусмотрен отдельный явный режим:

```bash
go run ./services/auth/cmd/auth-registration-backfill --limit=100 --apply --replay-published
```

Он переочередит опубликованные события, сохранив **исходные event ID и canonical payload**. Pending записи и активные lease не сбрасываются; сравнение исходного `published_at` защищает от устаревшего результата параллельной сверки. Издатель выполняет обычную доставку, а inbox User предотвращает изменение существующего профиля.

Учитывайте JetStream duplicate window: повтор с тем же ID внутри окна может получить успешный duplicate PubAck без нового сообщения, в том числе если исходное сообщение уже удалено. Для такого восстановления дождитесь окончания настроенного окна после последней публикации выбранных событий и выполните повторный полный проход `--replay-published`. Счётчик `Requeued` или успешный PubAck сами по себе не подтверждают наличие профиля. Проверяйте свежие показатели outbox/durable и [целостность inbox User](../user/README.md#наблюдение-и-сверка); существующие персональные поля никогда не перезаписываются.

Остановка команды оставляет уже созданные/переочереденные события в outbox; продолжение безопасно по cursor либо с начала. Для паузы доставки выключить publisher, сохранив capture и данные. Не очищать outbox/inbox и не применять down-миграции как способ отката backfill. Проверки команды с настоящей БД и доставки профиля входят в `task user:registration:integration`.

## Внутренний browser bridge

При включённых сессиях тот же защищённый mTLS listener дополнительно обслуживает `auth.v1.AuthBrowserService`: `BrowserRegisterCredentials`, `BrowserLogin`, `BrowserRefreshSession`, `BrowserLogout`, `BrowserLogoutAll`. Вызывать их может только workload `gateway-out` из настроенных trust domain и environment. Проверка peer выполняется как interceptor, так и самим адаптером; публичный HTTP listener этот сервис не регистрирует.

Приватные `Browser*Request` содержат исходный типизированный запрос и `BrowserContext`: отдельные строки `Cookie`, `Origin`, `Sec-Fetch-Site`. Для каждого поля разрешено не более 16 строк; суммарные пределы — 8192, 2048 и 256 байт соответственно. CR, LF и NUL запрещены. Cookie передаётся непрозрачно, дубликаты заголовков сохраняются. Адаптер вызывает существующий Connect handler, поэтому cookie parsing, rotation/revocation и Origin policy остаются едиными. При включённых сессиях регистрация также требует разрешённый Origin; дубликаты Origin и Sec-Fetch-Site отклоняются и на standalone surface.

`Browser*Response` передаёт исходный публичный ответ и отдельные строки `set_cookie` только по приватному каналу. Gateway обязан вернуть их как HTTP `Set-Cookie`, никогда как публичный JSON/protobuf body, и применить `Cache-Control: no-store`. Произвольные HTTP headers, URL, assertion или токены в публичном body контрактом не добавляются. Ошибки bridge содержат только безопасный gRPC status без внутренних деталей или metadata. Request/response bridge содержат секреты и не должны логироваться, трассироваться или кэшироваться.

## Почта и настройки безопасности

MM-90 реализует подтверждение почты, опциональный email-код покупателя, сброс и
смену пароля/email, список и отзыв своих сеансов, уведомления через шифрованный
mail outbox. Настройка, сроки, атомарность и ограничения отката описаны в
[Auth email](../../docs/security/auth-email.md). Локальный стенд автоматически
подключает Mailpit; после миграции 000004 отключение email-runtime запрещено.
