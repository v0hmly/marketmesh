# Platform

Общие технические Go-библиотеки MarketMesh. Здесь размещаются только повторно используемые инфраструктурные возможности без бизнес-моделей сервисов.

Доступные пакеты:

- [`logger`](logger/README.md) — типизированная обёртка над zerolog, структурный JSON без двойного кодирования, маскирование полей, slog-адаптер и корреляция с OpenTelemetry.
- [`telemetry`](telemetry/README.md) — изолированный OpenTelemetry pipeline для traces и metrics, OTLP/gRPC exporters и адаптеры ConnectRPC/gRPC.
- [`runtime`](runtime/README.md) — transport-agnostic env-конфигурация, безопасные секреты, readiness и ограниченный lifecycle.
- [`grpc`](grpc/README.md) — безопасные gRPC server/client, TLS/mTLS, обязательные deadlines, interceptors, standard health, ограниченные retry и lifecycle поверх runtime.
- [`postgres`](postgres/README.md) — явные RW/RO-пулы, транзакции, readiness и telemetry поверх pgx.

Пакеты создаются соответствующими задачами и не добавляются пустыми заранее.
