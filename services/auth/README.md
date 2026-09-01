# Auth

Auth владеет учётными данными и непрозрачными идентификаторами субъектов. Граница ответственности определена в [ADR-0006](../../docs/adr/0006-auth-and-user-bounded-contexts.md), а зависимости кода направлены по правилам [ADR-0011](../../docs/adr/0011-go-service-architecture.md).

Текущий вертикальный срез реализует регистрацию учётных данных и проверку логина. Создание профиля User, outbox-событие регистрации, сессии, MFA и восстановление учётной записи относятся к отдельным сценариям и не входят в MM-13.

## Границы и безопасность

- `internal/domain/credential` содержит чистые типы `Identifier`, `Password`, `SubjectID`, `PasswordDigest` и их инварианты.
- `internal/application/register` и `internal/application/login` — независимые сценарии с портами возле потребителя. Они не импортируют protobuf, pgx, logger или OpenTelemetry.
- `internal/adapter/in/connectrpc` отображает DTO и возвращает только стабильные публичные ошибки.
- `internal/adapter/out` содержит Argon2id, `crypto/rand`, PostgreSQL и аудит.
- `internal/app` вручную собирает зависимости, подключает `platform/logger`, `platform/telemetry`, `platform/postgres` и управляет graceful shutdown через `platform/runtime`.

Идентификатор нормализуется без сохранения исходного значения. Пароль ограничен 12–1024 байтами, копируется на доменной границе и очищается после использования. В БД сохраняется только PHC-строка Argon2id с версией и параметрами; для каждой записи соль читается из `crypto/rand`. При успешном входе устаревшие параметры обновляются compare-and-swap запросом.

Неизвестный идентификатор и неверный пароль дают одинаковый `Unauthenticated: invalid credentials`. Для неизвестного идентификатора выполняется Argon2id-проверка случайного process-local dummy digest. Повторная регистрация существующего идентификатора не отличается от успешной регистрации ни телом, ни кодом ответа и также проходит хэширование.

В логи, трассировки, метрики и клиентские ошибки не передаются identifier, password, digest, salt или payload. Аудит входа содержит только конечные категории `outcome` и `reason`; категории метрик имеют ограниченную кардинальность.

## API

Контракт находится в `api/proto/auth/v1/auth.proto` и воспроизводимо генерируется для Go/TypeScript:

- `RegisterCredentials` принимает identifier/password и возвращает пустой ответ без сигнала существования;
- `Login` принимает identifier/password и при успехе возвращает непрозрачный 16-байтовый `subject_id`.

Один handler поддерживает Connect, gRPC и gRPC-Web. Максимальный размер HTTP body по умолчанию — 16 КиБ.

## PostgreSQL

Миграции находятся в `migrations` и применяются отдельным доверенным migration job:

1. `000001_credentials.up.sql` создаёт схему `auth` и таблицу `auth.credentials`;
2. application role получает только необходимые права на эту схему;
3. сервис запускается с отдельными `POSTGRES_RW_DSN` и `POSTGRES_RO_DSN` своей Auth БД.

Сервис намеренно не применяет миграции при старте. Записи и security-sensitive чтения входа используют только RW executor, чтобы replica lag не влиял на решение аутентификации. Запросы статические и параметризованные, конкурентная регистрация разрешается `UNIQUE (identifier)` и `ON CONFLICT DO NOTHING`.

## Конфигурация

Обязательные переменные:

| Переменная | Назначение |
| --- | --- |
| `SERVICE_VERSION` | версия процесса |
| `ENVIRONMENT` | окружение |
| `SERVICE_INSTANCE_ID` | идентификатор экземпляра и часть `application_name` PostgreSQL |
| `POSTGRES_RW_DSN` | секретный DSN primary Auth |
| `POSTGRES_RO_DSN` | секретный DSN replica Auth для readiness и будущих eventual-consistent чтений |

Основные ограниченные настройки:

| Переменная | По умолчанию |
| --- | --- |
| `HTTP_ADDRESS` | `127.0.0.1:8081` |
| `HTTP_MAX_BODY_BYTES` | `16384` |
| `HTTP_REQUEST_TIMEOUT` | `10s` |
| `POSTGRES_MAX_CONNS` | `10` |
| `POSTGRES_QUERY_TIMEOUT` | `3s` |
| `ARGON2_MEMORY_KIB` | `65536` |
| `ARGON2_TIME` | `3` |
| `ARGON2_PARALLELISM` | `2` |
| `ARGON2_SALT_BYTES` | `16` |
| `ARGON2_KEY_BYTES` | `32` |

Остальные HTTP, health, shutdown, PostgreSQL pool и OTLP настройки следуют тем же именам и безопасным значениям по умолчанию, что и platform runtime. HTTP limits, deadline, recovery, telemetry и graceful shutdown предоставляет `platform/httpserver`.

## Проверка

```bash
go test ./services/auth/...
go test -race ./services/auth/...
task auth:integration
task verify
govulncheck ./...
```

`task auth:integration` поднимает одноразовый PostgreSQL в изолированной Docker-сети, применяет migration и проверяет lifecycle репозитория и 32 конкурентные регистрации одного identifier.
