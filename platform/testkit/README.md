# Testkit

`testkit` содержит только повторно используемую тестовую инфраструктуру Go-модулей MarketMesh. Production-код не должен импортировать этот пакет; запрет проверяется `task arch`.

## Lifecycle и параллельность

Все конструкторы принимают `testing.TB`, вызывают `Helper` и регистрируют освобождение ресурсов через `Cleanup`. Экземпляры не используют изменяемое глобальное состояние и безопасны для независимых `t.Parallel` тестов.

Доступные helpers:

- `NewLogger` — logger MarketMesh с конкурентно-безопасным захватом JSON-событий и штатным маскированием чувствительных полей;
- `NewTelemetry` и `NoopTelemetry` — изолированные in-memory/no-op OpenTelemetry pipelines;
- `NewTLS` — временный in-memory CA и server/client сертификаты для TLS и mTLS;
- `NewBufconn` — in-memory gRPC harness с единым connection и гарантированным shutdown;
- `NewClock` — управляемые fake clock, timer и ticker без process-global состояния;
- `Wait` и `Eventually` — bounded ожидания без необоснованных `time.Sleep`;
- `TempDir` и `TempFile` — временные пути с режимами `0700` и `0600` и защитой от path traversal;
- `testkit/integration.EnvOrSkip` — чтение обязательного integration environment; пакет доступен только с `-tags=integration`.

Пакет намеренно не предоставляет assertion framework, mocking framework, доменные fixtures, скрытый Docker lifecycle, изменение process environment или helpers единственного потребителя.

## Проверка

```bash
cd platform
GOWORK=off go test -race ./testkit/...
GOWORK=off go test -race -tags=integration ./testkit/...
```
