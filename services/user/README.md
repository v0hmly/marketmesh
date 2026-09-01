# User

Сервис владеет профилем, настройками и пользовательскими атрибутами, которые не относятся к учётным данным входа. Он не имеет доступа к хранилищу Auth.

Граница ответственности определена в [ADR-0006](../../docs/adr/0006-auth-and-user-bounded-contexts.md).

## Минимальный runtime

Сервис содержит минимальный composition root без бизнес-логики:

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
