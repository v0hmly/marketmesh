// Package postgres предоставляет явные RW/RO-пулы PostgreSQL, ограниченные
// query timeout, readiness checks, транзакции с безопасными повторами и
// OpenTelemetry-инструментацию поверх pgx/pgxpool.
//
// Пакет не маршрутизирует SQL автоматически, не скрывает SQL за ORM и не
// определяет бизнес-репозитории. Исходящие порты остаются у application-кода,
// а pgx-зависимые adapters используют Executor только внутри adapter boundary.
package postgres
