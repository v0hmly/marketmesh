# PostgreSQL для Go-сервисов

Пакет `postgres` предоставляет небольшую явную обвязку над `pgx/pgxpool` v5:

- отдельные обязательные RW- и RO-пулы;
- проверку, что RW endpoint ведёт на primary, а RO — на hot standby;
- query timeout, действующий до закрытия `Rows` или вызова `Row.Scan`;
- транзакции только на RW с `READ COMMITTED`, `REPEATABLE READ` или `SERIALIZABLE`;
- запрещённые вложенные транзакции;
- bounded retry всей транзакции только после явного `Idempotent: true` и только для SQLSTATE `40001`/`40P01`;
- readiness, pool metrics и traces без SQL, arguments, DSN и PII;
- управляемое закрытие через `platform/runtime.Component`.

Пакет намеренно не предоставляет ORM, SQL builder, универсальный repository, скрытый выбор replica и автоматический разбор SQL. Предметные исходящие порты остаются у application-потребителя; `Executor` используется только внутри PostgreSQL adapter.

## Создание

```go
env := runtime.SystemEnv()
rwDSN, err := env.Secret("POSTGRES_RW_DSN", true)
if err != nil {
	return err
}
roDSN, err := env.Secret("POSTGRES_RO_DSN", true)
if err != nil {
	return err
}

poolConfig := func(dsn runtime.Secret) postgres.PoolConfig {
	return postgres.PoolConfig{
		DSN:                   dsn,
		MaxConns:              20,
		MinConns:              2,
		MinIdleConns:          2,
		ConnectTimeout:        5 * time.Second,
		QueryTimeout:          2 * time.Second,
		MaxConnLifetime:       30 * time.Minute,
		MaxConnLifetimeJitter: 3 * time.Minute,
		MaxConnIdleTime:       5 * time.Minute,
		HealthCheckPeriod:     30 * time.Second,
		PingTimeout:           time.Second,
	}
}

database, err := postgres.New(ctx, postgres.Config{
	RW: poolConfig(rwDSN),
	RO: poolConfig(roDSN),
	Retry: &postgres.RetryPolicy{
		MaxAttempts:       3,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2,
	},
}, telemetryPipeline)
if err != nil {
	return err
}
```

DSN передаётся только как `runtime.Secret`. `New` раскрывает значение непосредственно перед `pgxpool.ParseConfig`, но собственные errors, metrics и traces значение не содержат. Исходные ошибки PostgreSQL сохраняются для `errors.Is`/`errors.As`, а безопасная внешняя формулировка `operationError` не печатает driver details.

## Явный RW/RO

```go
// Запись и read-after-write используют primary.
_, err := database.RW().Exec(ctx, insertUserSQL, userID, email)

// Только явно eventual-consistent чтение использует replica.
row := database.RO().QueryRow(ctx, listPublicProfileSQL, userID)
```

Конструктор выполняет `pg_is_in_recovery()` и проверяет `transaction_read_only`. Переставленные DSN или RO endpoint, указывающий не на standby, отклоняются при старте. Пакет не перемещает запросы между пулами автоматически.

`Executor` повторяет минимальные `Exec`, `Query` и `QueryRow` сигнатуры pgx. Domain и public application API его не импортируют: PostgreSQL adapter реализует предметный порт application и скрывает row/driver details.

## Транзакции

```go
err := database.WithinTransaction(ctx, postgres.TransactionOptions{
	Isolation:  postgres.IsolationSerializable,
	Idempotent: true,
}, func(ctx context.Context, tx postgres.Executor) error {
	repository := userpostgres.NewRepository(tx)
	return repository.Create(ctx, user)
})
```

Callback получает transaction-scoped `Executor` и context marker. Повторный `WithinTransaction` с этим context возвращает `ErrNestedTransaction`: неявные savepoint и автоматическое переиспользование внешней транзакции запрещены. Callback должна передать именно полученные `ctx` и `tx` всем adapters; захватывать `database.RW()` внутри callback нельзя.

Retry выключен, если `RetryPolicy == nil`, и никогда не применяется без `Idempotent: true`. При `40001` или `40P01` заново выполняется вся callback с новым `pgx.Tx`; число попыток ограничено пятью, backoff ограничен `MaxBackoff`, cancellation останавливает ожидание. Иные SQLSTATE, сетевые ошибки и неидемпотентные операции не повторяются. После исчерпания возвращается `ErrRetryExhausted` вместе с исходным `*pgconn.PgError`.

Callback error и commit error сохраняются. Callback error вызывает rollback с cleanup context, не отменённым вместе с запросом. Panic также вызывает rollback и затем пробрасывается без преобразования.

## Readiness и lifecycle

```go
health, err := runtime.NewHealth(runtime.HealthConfig{
	CheckTimeout: 2 * time.Second,
	Dependencies: database.ReadinessDependencies(),
})
if err != nil {
	return err
}

databaseComponent, err := database.Component("postgres")
```

Рекомендуемый порядок компонентов: telemetry → PostgreSQL → transports. `Runner` остановит их в обратном порядке: transport перестанет принимать работу до закрытия pools, а telemetry останется доступной до завершения PostgreSQL.

`pgxpool.Close` не принимает context и ждёт возврата захваченных connections. `Database.Close` запускает закрытие RW/RO независимо и возвращает `context.DeadlineExceeded`, если общий shutdown deadline истёк. В таком случае поздно возвращённое connection всё равно будет закрыто фоновой операцией; код adapters обязан всегда закрывать `Rows` и освобождать acquired connections.

## Telemetry

Query spans называются `postgres.query` и содержат только `db.system.name=postgresql`, `postgres.pool=rw|ro` и безопасный SQLSTATE при ошибке. SQL, arguments, server error text и DSN не записываются.

Пакет экспортирует OTel instruments:

- `marketmesh.postgres.connections.{total,idle,acquired,constructing,max}`;
- cumulative acquire/create/destroy counters и acquire wait duration;
- `marketmesh.postgres.transaction.{attempts,retries,duration}`.

Единственные labels: `postgres.pool`, isolation, read-only flag, bounded result и retry reason. Примеры после Prometheus-normalization:

```promql
marketmesh_postgres_connections_acquired / marketmesh_postgres_connections_max
rate(marketmesh_postgres_transaction_retries_total[5m])
histogram_quantile(0.99, sum(rate(marketmesh_postgres_transaction_duration_seconds_bucket[5m])) by (le))
```

## Проверки

Обычные unit-тесты герметичны и входят в `task verify`. Интеграционные тесты используют существующую таблицу `public.infra_smoke` из MM-9, не создают схему и проверяют primary, synchronous replica, commit, rollback, read-only mode и cancellation:

```bash
task postgres:integration
```

Docker test runner сначала собирается с зафиксированной версией Go, затем запускается только во внутренней Compose-сети. Пароли передаются через `infra/compose/.env` и не печатаются тестами.
