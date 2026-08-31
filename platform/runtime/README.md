# Runtime Go-сервиса

Пакет `runtime` предоставляет небольшой общий слой для запуска Go-сервисов MarketMesh. Он управляет только конфигурацией, health probes и жизненным циклом. Регистрация RPC, маршруты, бизнес-логика, listener, TLS и ручная сборка графа зависимостей остаются в `services/<service>/internal/app`.

Пакет не меняет process-wide logger, OpenTelemetry providers или другие глобальные singleton и ничего не запускает через `init`.

## Environment и секреты

`Env` получает функцию с сигнатурой `os.LookupEnv`. Production использует `SystemEnv`, тесты — `MapEnv` или `NewEnv` с локальной функцией. Поэтому параллельные тесты конфигурации не вызывают `os.Setenv` и не разделяют process environment.

```go
env := runtime.SystemEnv()

version, err := env.RequiredString("SERVICE_VERSION")
shutdownTimeout, err := env.PositiveDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
token, err := env.Secret("OTEL_AUTH_TOKEN", false)
```

Ошибки парсинга содержат имя variable и ожидаемый тип, но не исходное значение. `Secret` маскируется при `fmt`, `log/slog`, text и JSON serialization. Получить исходную строку можно только явным `Reveal`; её запрещено помещать в error, log, trace или metric.

## Lifecycle

`Component` состоит из стабильного имени, блокирующего `Run` и ограниченного `Shutdown`. `Runner` запускает компоненты параллельно, а останавливает последовательно в обратном порядке с одним общим deadline. Первый неожиданный выход компонента инициирует остановку остальных. Ошибки выполнения и остановки объединяются через `errors.Join` и возвращаются composition root без логирования внутри пакета.

```go
runner, err := runtime.NewRunner(
	runtime.RunnerConfig{
		ShutdownTimeout: 15 * time.Second,
		Health:          health,
	},
	telemetryComponent, // запускается первой, останавливается последней
	httpComponent,
)
if err != nil {
	return err
}

// Сигнал уже преобразован в cancellation корневого ctx в cmd/<service>.
return runner.Run(ctx)
```

Компонент обязан завершать `Run` после cancellation и учитывать deadline в `Shutdown`. Если реализация игнорирует context, `Runner` всё равно вернётся после общего deadline; Go не позволяет принудительно завершить зависшую goroutine, поэтому такая реализация остаётся дефектной.

Возвращённая ошибка логируется ровно один раз в `internal/app`. `cmd/<service>` использует её только для ненулевого process exit code.

## Liveness и readiness

Новый `Health` сначала жив, но не готов. `Runner` вызывает `MarkReady` после запуска компонентов и `MarkNotReady` до их cancellation. Liveness не зависит от readiness и критических зависимостей, поэтому временная недоступность БД не провоцирует бессмысленный restart процесса.

```go
health, err := runtime.NewHealth(runtime.HealthConfig{
	CheckTimeout: 2 * time.Second,
	Dependencies: []runtime.CriticalDependency{
		{
			Name:  "database",
			Check: database.PingContext,
		},
	},
})

mux.Handle("GET /livez", health.LivenessHandler())
mux.Handle("GET /readyz", health.ReadinessHandler())
```

HTTP probe никогда не возвращает наружу ошибку зависимости. Все checks получают общий ограниченный context и должны прекращать работу после его cancellation.

## HTTP

`NewHTTPServer` требует явно положительные `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes` и `MaxBodyBytes`. Последний применяется через `http.MaxBytesHandler`. Пакет возвращает обычный `*http.Server`; TLS и listener остаются видимы в composition root.

`NewHTTPComponent` адаптирует готовые server и listener к `Component` и использует `http.Server.Shutdown`.

## gRPC

`NewGRPCServer` требует connection timeout, максимальный RPC timeout, keepalive timeout и пределы входящего/исходящего сообщения. Встроенные unary/stream interceptors добавляют deadline только если клиентский deadline отсутствует или длиннее разрешённого. Дополнительные interceptors и telemetry `StatsHandler` передаются явно через `GRPCServerConfig`.

```go
server, err := runtime.NewGRPCServer(runtime.GRPCServerConfig{
	ConnectionTimeout:      5 * time.Second,
	RequestTimeout:         10 * time.Second,
	KeepaliveTime:          30 * time.Second,
	KeepaliveTimeout:       10 * time.Second,
	MaxReceiveMessageBytes: 4 << 20,
	MaxSendMessageBytes:    4 << 20,
	StatsHandler:           pipeline.GRPCServerStatsHandler(),
})
```

TLS credentials передаются полем `Credentials`; production gRPC без TLS допустим только за доверенной границей, где шифрование обеспечивает согласованный service mesh. `NewGRPCComponent` выполняет `GracefulStop`, а по истечении deadline вызывает `Stop`.

## Пример интеграции

Минимальная интеграция находится в [`services/user/internal/app`](../../services/user/internal/app). `main` создаёт signal-aware root context и делегирует запуск `app.Run`; `internal/app` явно создаёт config, logger, telemetry, health, listener, HTTP server и `Runner`. Бизнес-логики в примере нет.
