// Package runtime предоставляет минимальные примитивы жизненного цикла
// Go-сервисов MarketMesh: типизированное чтение environment, безопасные
// секретные значения, health checks, ограниченный graceful shutdown и узкие
// адаптеры стандартных HTTP- и gRPC-серверов.
//
// Пакет не регистрирует глобальные обработчики, providers или singleton.
// Конкретный сервис явно собирает зависимости в internal/app.
package runtime
