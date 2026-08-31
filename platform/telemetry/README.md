# Telemetry

Пакет `telemetry` создаёт изолированный OpenTelemetry pipeline для traces и metrics. Production-режим отправляет оба сигнала в OpenTelemetry Collector по OTLP/gRPC. Пакет также предоставляет готовые адаптеры для ConnectRPC и gRPC.

Логи остаются ответственностью пакета [`logger`](../logger). `telemetry` не импортирует его и не меняет глобальные providers OpenTelemetry, поэтому экземпляры сервисов и параллельные тесты не влияют друг на друга.

## Быстрый старт

```go
package main

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

func run(ctx context.Context, log *logger.Logger) error {
	// SDK сообщает фоновые ошибки через глобальный handler. Его настраивает
	// только composition root приложения, используя наш logger.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Error("ошибка OpenTelemetry pipeline", logger.Err(err))
	}))

	sampleRatio := 0.1
	pipeline, err := telemetry.New(ctx, telemetry.Config{
		ServiceName:          "auth",
		ServiceVersion:       "1.0.0",
		Environment:          "production",
		InstanceID:           "auth-7f6d9d9c8f-x2k9p",
		Endpoint:             "otel-collector.internal:4317",
		TraceSampleRatio:     &sampleRatio,
		ExportTimeout:        5 * time.Second,
		MetricExportInterval: 30 * time.Second,
	})
	if err != nil {
		return err
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := pipeline.Shutdown(shutdownCtx); err != nil {
			log.Error("не удалось остановить telemetry", logger.Err(err))
		}
	}()

	// Providers передаются компонентам явно:
	tracer := pipeline.Tracer("github.com/v0hmly/marketmesh/services/auth")
	_, span := tracer.Start(ctx, "bootstrap")
	defer span.End()

	return nil
}
```

`Endpoint` задаётся как `host:port` без `http://` или `https://`. По умолчанию используется TLS не ниже 1.2. `Insecure: true` допустим только для локального Collector. `TLSConfig` копируется; `InsecureSkipVerify` запрещён.

`TraceSampleRatio == nil` означает `1.0`. Чтобы отключить новые корневые traces, передайте указатель на `0.0`. Sampler является `ParentBased`: на доверенных внутренних границах решение родительского span сохраняется.

## Транспортные границы

ConnectRPC в `gateway-in`, принимающий запросы из DMZ, должен использовать `PublicConnectInterceptor`. Внешний `traceparent` становится link нового server span и не управляет его родительством или sampling. Для доверенного межсервисного ConnectRPC существует `TrustedConnectInterceptor`. Оба варианта отключают события на каждое сообщение: для streaming RPC, включая чат, это предотвращает неконтролируемый рост spans и накладных расходов.

```go
publicInterceptor, err := pipeline.PublicConnectInterceptor()
if err != nil {
	return err
}

handler := connect.NewUnaryHandler(
	procedure,
	service.Handle,
	connect.WithInterceptors(publicInterceptor),
)
```

Внутренние gRPC-серверы и клиенты получают explicit stats handlers:

```go
server := grpc.NewServer(
	grpc.StatsHandler(pipeline.GRPCServerStatsHandler()),
)

connection, err := grpc.NewClient(
	target,
	grpc.WithStatsHandler(pipeline.GRPCClientStatsHandler()),
)
```

Аутентификация, mTLS и авторизация остаются отдельными механизмами безопасности. `traceparent` является корреляционным контекстом, а не идентификатором пользователя или доказательством доверия.

## Propagation и данные

Пакет распространяет только W3C Trace Context. Baggage намеренно не включён: произвольные значения из запросов не должны автоматически перемещаться между зонами.

Запрещено добавлять в span attributes и metric labels:

- токены, cookie, пароли, e-mail и другие PII;
- `user_id`, `session_id`, `request_id` и иные почти уникальные значения;
- полный URL, query string, произвольный User-Agent и текст ошибки;
- необработанные заголовки запроса или ответа.

Для metrics используются нормализованные значения с ограниченным набором вариантов: имя RPC-сервиса и метода, код ответа, HTTP method и route template. Лимит cardinality по умолчанию равен 2000 series на instrument, но он служит последней защитой и не заменяет правильный выбор labels.

## Тесты и локальная разработка

Для тестов без экспорта используйте `NewNoop`. Если нужно проверить записанные данные, передайте `WithSpanExporter` и `WithMetricReader`; в этом случае Collector не требуется.

```go
spanExporter := tracetest.NewInMemoryExporter()
metricReader := sdkmetric.NewManualReader()

pipeline, err := telemetry.New(
	context.Background(),
	config,
	telemetry.WithSpanExporter(spanExporter),
	telemetry.WithMetricReader(metricReader),
)
```

После успешного `New` пакет владеет переданными exporter и reader. Каждый рабочий процесс обязан вызвать `Shutdown` с ограниченным deadline. Ошибки остановки traces и metrics объединяются через `errors.Join` и не теряются.
