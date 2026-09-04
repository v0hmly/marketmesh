# Platform

Общие технические Go-библиотеки MarketMesh. Здесь размещаются только повторно используемые инфраструктурные возможности без бизнес-моделей сервисов.

Доступные пакеты:

- [`logger`](logger/README.md) — типизированная обёртка над zerolog, структурный JSON без двойного кодирования, маскирование полей, slog-адаптер и корреляция с OpenTelemetry.
- [`telemetry`](telemetry/README.md) — изолированный OpenTelemetry pipeline для traces и metrics, OTLP/gRPC exporters и адаптеры ConnectRPC/gRPC.
- [`runtime`](runtime/README.md) — transport-agnostic env-конфигурация, безопасные секреты, readiness и ограниченный lifecycle.
- [`grpc`](grpc/README.md) — безопасные gRPC server/client, TLS/mTLS, обязательные deadlines, interceptors, standard health, ограниченные retry и lifecycle поверх runtime.
- [`httpserver`](httpserver/README.md) — безопасный net/http server, request middleware, HTTP health и bounded lifecycle поверх runtime.
- [`postgres`](postgres/README.md) — явные RW/RO-пулы, транзакции, readiness и telemetry поверх pgx.
- [`redis`](redis/README.md) — независимые edge/auth clients, bounded pool/retry, readiness и безопасная telemetry поверх go-redis.
- [`testkit`](testkit/README.md) — повторно используемые lifecycle-safe helpers для logger/telemetry, TLS/mTLS, bufconn, fake time, bounded wait и безопасных временных путей; production imports запрещены архитектурной проверкой.
- [`sessionassert`](sessionassert/README.md) — подписанные Ed25519 внутренние session assertions по ADR-0005: выпуск с фиксированной аудиторией и локальная проверка по набору открытых ключей с ротацией в перекрытие; только стандартная библиотека.
- `workloadid` — машинная идентичность рабочих нагрузок и авторизация внутренних RPC по ADR-0004: SPIFFE-совместимые URI SAN (среда + роль), извлечение идентичности из mTLS-контекста, fail-closed политика «кто может вызвать что», отзыв по серийному номеру и unary/stream interceptors для gRPC.

Пакеты создаются соответствующими задачами и не добавляются пустыми заранее.
