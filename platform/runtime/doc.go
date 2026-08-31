// Package runtime предоставляет минимальные примитивы жизненного цикла
// Go-сервисов MarketMesh: типизированное чтение environment, безопасные
// секретные значения, transport-agnostic readiness checks и ограниченный
// graceful shutdown.
//
// Пакет не зависит от конкретного сетевого transport, не регистрирует
// глобальные обработчики, providers или singleton. Конкретный сервис явно
// собирает зависимости и transport adapters в internal/app.
package runtime
