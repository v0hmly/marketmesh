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
- `internal/adapter/out` содержит Argon2id, PostgreSQL, Redis и управление
  ключами session assertions.
- `internal/app` вручную собирает зависимости, подключает runtime, telemetry и
  graceful shutdown.

Идентификатор нормализуется без сохранения исходного значения. Пароль ограничен
12–1024 байтами, копируется на доменной границе и очищается после использования.
В БД сохраняется только PHC-строка Argon2id; соль берётся из `crypto/rand`.
Неизвестный идентификатор и неверный пароль дают одинаковый
`Unauthenticated: invalid credentials`, включая Argon2id-проверку process-local
dummy digest для неизвестного идентификатора. Повторная регистрация существующего
identifier не отличается от успешной регистрации ни телом, ни кодом ответа и
также проходит хэширование. При успешном входе устаревшие параметры Argon2id
обновляются compare-and-swap запросом.

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
| `ARGON2_MEMORY_KIB` | `65536` |
| `ARGON2_TIME` | `3` |
| `ARGON2_PARALLELISM` | `2` |
| `ARGON2_SALT_BYTES` | `16` |
| `ARGON2_KEY_BYTES` | `32` |

Также требуются общие `SERVICE_VERSION`, `ENVIRONMENT`, `SERVICE_INSTANCE_ID`,
`POSTGRES_RW_DSN` и `POSTGRES_RO_DSN`. Для включённых сессий обязательны
`AUTH_REDIS_ADDRESS` и `AUTH_REDIS_PASSWORD`; по умолчанию нужен проверенный
Redis TLS (`AUTH_REDIS_TLS_SERVER_NAME`, опционально `AUTH_REDIS_CA_FILE`).
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
