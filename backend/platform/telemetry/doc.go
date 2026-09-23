// Package telemetry предоставляет изолированный OpenTelemetry pipeline для
// traces и metrics, OTLP/gRPC exporters и транспортные адаптеры Connect/gRPC.
//
// Пакет не изменяет глобальные providers OpenTelemetry и не зависит от
// бизнес-моделей или logger. Для локального запуска без Collector предназначен
// NewNoop.
package telemetry
