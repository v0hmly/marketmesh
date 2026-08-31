# Runtime Go-сервиса

Пакет `runtime` предоставляет небольшой transport-agnostic слой для запуска Go-сервисов MarketMesh. Он управляет только конфигурацией, readiness и жизненным циклом. HTTP, gRPC, регистрация маршрутов и RPC, listener, TLS и ручная сборка графа зависимостей остаются в transport-библиотеках и `services/<service>/internal/app`.

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
	transportComponent,
)
if err != nil {
	return err
}

// Сигнал уже преобразован в cancellation корневого ctx в cmd/<service>.
return runner.Run(ctx)
```

Компонент обязан завершать `Run` после cancellation и учитывать deadline в `Shutdown`. Если реализация игнорирует context, `Runner` всё равно вернётся после общего deadline; Go не позволяет принудительно завершить зависшую goroutine, поэтому такая реализация остаётся дефектной.

Возвращённая ошибка логируется ровно один раз в `internal/app`. `cmd/<service>` использует её только для ненулевого process exit code.

## Readiness

Новый `Health` сначала не готов. `Runner` вызывает `MarkReady` после запуска компонентов и `MarkNotReady` до их cancellation. Конкретный transport adapter вызывает `Ready(ctx)` и самостоятельно преобразует результат в свой протокол, не раскрывая детали ошибок зависимости.

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

if err := health.Ready(ctx); err != nil {
	// Transport adapter возвращает только безопасный protocol status.
}
```

Все checks получают общий ограниченный context и должны прекращать работу после его cancellation. Liveness является свойством процесса и transport/deployment adapter; временная неготовность зависимости не должна провоцировать бессмысленный restart процесса.

## Пример интеграции

Минимальная интеграция находится в [`services/user/internal/app`](../../services/user/internal/app). `main` создаёт signal-aware root context и делегирует запуск `app.Run`; `internal/app` явно создаёт config, logger, telemetry, health, transport adapters и `Runner`. Бизнес-логики в примере нет.
