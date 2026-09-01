# ADR-0013: Доступ к PostgreSQL и границы транзакций

- Статус: Принято
- Дата: 2026-09-01
- Авторы: команда проекта
- Заменяет: нет
- Заменено решением: нет

## Контекст

Go-сервисам MarketMesh нужен общий production-ready способ подключаться к PostgreSQL из MM-9. Инфраструктура предоставляет primary через `postgres-rw` и synchronous hot standby через `postgres-ro`. Требуются явный выбор консистентности, bounded I/O, транзакции, readiness и telemetry без переноса SQL и бизнес-репозиториев в platform.

Нужно выбрать между нативным `pgx/pgxpool` и дополнительной обёрткой. Важны лицензия, сопровождение, стабильность API, dependency graph, история уязвимостей и возможность обновлять driver без собственного RPC/ORM-фреймворка.

На дату решения актуальная стабильная линия `pgx` — v5, выбранная версия — v5.10.0. Проект имеет MIT-лицензию, следует semantic versioning для документированного stable API, активно сопровождается и поддерживает актуальные Go/PostgreSQL. В 2026 году в старых версиях v5 были исправлены memory-safety проблемы до v5.9.0 и SQL injection в non-default simple protocol до v5.9.2; v5.10.0 дополнительно усилила декодирование, authentication negotiation и TLS cancellation. Поэтому использование старой minor/patch линии неприемлемо.

## Решение

Использовать `github.com/jackc/pgx/v5` и `pgxpool` напрямую, без ORM, `database/sql`, `sqlx`, `scany` и внешней pgx telemetry-обёртки.

В `platform/postgres` реализуется небольшая явная обвязка:

- обязательные независимые RW- и RO-пулы с отдельным lifecycle;
- обязательный общий `application_name`, явно переданный composition root,
  нормализованный как печатный ASCII длиной не более 63 байт и принудительно
  установленный после разбора DSN для обоих пулов;
- `runtime.Secret` для DSN;
- проверка primary/hot standby через `pg_is_in_recovery()` и `transaction_read_only`;
- обязательные connect/query/ping timeout и pool lifetime/idle limits;
- явные `RW()` и `RO()` без SQL parsing, fallback и автоматической маршрутизации;
- минимальный pgx-compatible `Executor` только для PostgreSQL adapters;
- `WithinTransaction`, всегда начинающий транзакцию на RW;
- уровни `READ COMMITTED`, `REPEATABLE READ`, `SERIALIZABLE` и read-only mode;
- запрет вложенных транзакций; скрытые savepoint и автоматическое переиспользование отсутствуют;
- ограниченный retry всей callback только при явной идемпотентности и SQLSTATE `40001`/`40P01`;
- сохранение исходной error chain для `errors.Is`/`errors.As` при безопасном стабильном тексте platform errors;
- readiness обоих пулов и bounded lifecycle через `platform/runtime`;
- собственная небольшая pgx `QueryTracer` и OTel pool/transaction metrics через явно переданный `platform/telemetry`, без process-wide globals;
- запрет SQL, arguments, DSN, error details, PII и high-cardinality values в telemetry.

Transaction boundary принадлежит application use case. Platform предоставляет механизм, но не универсальный business `Transactor` или repository. Application callback получает transaction-scoped executor и передаёт его конкретным outbound adapters. Domain и public application API не импортируют pgx.

Read-after-write всегда использует RW. RO предназначен только для явно eventual-consistent чтений; автоматического переноса запросов на replica нет.

## Рассмотренные альтернативы

### `database/sql` + `sqlx`

`sqlx` имеет MIT-лицензию, зрелый стабильный API и удобные named queries/struct scanning. В MarketMesh используется только PostgreSQL, поэтому `database/sql` добавляет лишний adapter layer и лишает код прямого доступа к нативным pgx features. Struct mapping и named queries не нужны платформенному lifecycle/transaction слою и должны решаться конкретным adapter при появлении реальной повторяемости. Отклонено как лишняя обвязка без доказанного преимущества.

### `pgx` + `scany/pgxscan`

`scany` имеет MIT-лицензию, поддерживает pgx и сокращает reflection-based struct scanning. Он не решает pool lifecycle, RW/RO, transaction boundary, retry, readiness или telemetry, зато увеличивает dependency и public surface. Нативный pgx уже предоставляет `CollectRows` и `RowToStruct*`, а adapters могут использовать явный `Scan`. Отклонено до появления повторяющейся измеримой проблемы mapping.

### ORM (`GORM`, `ent`) или универсальный repository

ORM добавляет query/schema abstractions, hooks и migration coupling, не требуемые MM-18. Это скрывает SQL и конфликтует с ADR-0011, где outbound ports предметны и принадлежат application-потребителю. Отклонено.

### Внешняя OpenTelemetry-обёртка pgx

Готовые pgx instrumentation packages обычно записывают SQL statement или требуют отдельной конфигурации sanitization и новой dependency. Для MM-18 достаточно короткой реализации `pgx.QueryTracer` и callbacks `pgxpool.Stat`; она гарантирует отсутствие SQL/arguments и использует существующие изолированные providers. Отклонено как не дающее доказанного преимущества.

### Вложенные транзакции через pgx savepoint

Savepoint меняет semantics отказа и может создать ложное ожидание независимой вложенной commit boundary. Автоматическое переиспользование executor через context также скрывает dependency. Выбран fail-fast `ErrNestedTransaction`; savepoint можно добавить отдельным решением при конкретном use case.

## Последствия

### Положительные

- Один поддерживаемый PostgreSQL-native driver и небольшой dependency graph.
- Явная consistency boundary между primary и replica.
- Транзакции не могут случайно начаться на RO.
- Retry ограничен, наблюдаем и требует явной идемпотентности.
- Driver errors остаются доступны через `errors.Is`/`errors.As`.
- Lifecycle и telemetry соответствуют существующим platform packages без глобального состояния.

### Отрицательные и риски

- `Executor` содержит pgx types и допустим только внутри outbound adapter; нарушение границы создаст driver coupling application/domain.
- `pgxpool.Close` не принимает context. Platform ограничивает ожидание deadline, но background close завершится только после возврата захваченных connections.
- Явный `Scan` может быть многословным; дополнительный mapper оценивается только по данным нескольких adapters.
- Неправильно объявленная идемпотентность может повторить side effect вне PostgreSQL; callback не должна выполнять необратимые внешние действия.
- RO может отставать; synchronous replica MM-9 уменьшает окно для подтверждённых записей, но application всё равно обязана выбирать RW для read-after-write.

## Защитные и эксплуатационные требования

- Pin конкретной стабильной версии pgx; запрещены `latest` и непроверенные pseudo-version.
- Patch с security fix обновляется приоритетно после `govulncheck`, changelog review, unit/race и integration tests.
- Minor update v5 проходит changelog review и полный CI; major update требует отдельной задачи и проверки ADR.
- Не использовать `QueryExecModeSimpleProtocol` без отдельного обоснования и security review.
- DSN создаётся только как `runtime.Secret`; SQL, arguments и driver error text не записываются в logs/metrics/traces.
- Local `application_name` равен точному имени сервиса. Kubernetes использует
  `<pod>/<namespace>/<cluster>`: pod и namespace поступают через Downward API,
  cluster задаётся отдельно, а composition root соблюдает общий 63-byte budget.
- `platform/postgres` не читает process environment. Канонический
  `application_name` явно переопределяет значения DSN и `PGAPPNAME`, не
  усекается и не используется как high-cardinality metric или trace attribute.
- Все операции получают caller context; `Rows` обязательно закрываются.
- RW/RO pool sizes согласуются с лимитом PostgreSQL на сумму replicas сервисов, а не на один process изолированно.
- Readiness проверяет оба критичных endpoint. Сервис, которому RO не критичен, должен принять это отдельным application/deployment решением, а не молча скрывать ошибку.
- Порядок lifecycle: telemetry → PostgreSQL → transports, shutdown в обратном порядке.

## Проверяемые инварианты

- Unit-тесты отклоняют небезопасные pool/retry/application name настройки, не
  раскрывают DSN и подтверждают precedence канонического имени над DSN и
  `PGAPPNAME`.
- Endpoint role mismatch отклоняется при `New`.
- Транзакция всегда вызывает `BeginTx` только на RW backend.
- Callback error, commit, rollback, panic и cancellation покрыты тестами.
- SQLSTATE `40001` и `40P01` повторяются только при `Idempotent: true`; attempts/backoff ограничены.
- `errors.As` извлекает исходный `*pgconn.PgError` после platform wrapping и exhaustion.
- Traces/metrics tests не находят SQL, arguments, DSN и server error text.
- Integration tests против MM-9 подтверждают одинаковый `application_name` для
  RW/RO, commit/rollback, отказ write в read-only mode и видимость commit на
  synchronous RO replica.
- `gofmt`, `go vet`, `go test -race`, `govulncheck`, `go mod verify` и обязательный workspace verify успешны.

## Отложенные вопросы

- Общий application-level transaction/outbox port после первой доменной реализации PostgreSQL.
- Savepoint только при конкретном use case с явно определёнными semantics.
- SQL mapper/code generation только после повторения boilerplate и отдельной оценки dependency.
- Production TLS/mTLS и certificate rotation вместе с workload identity и deployment configuration.

## Связанные документы

- [ADR-0011: Гексагональная архитектура Go-микросервисов](0011-go-service-hexagonal-architecture.md)
- [ADR-0012: Структура монорепозитория и Go workspace](0012-monorepository-and-go-workspace.md)
- [Локальная инфраструктура](../../infra/compose/README.md)
- [PostgreSQL driver and toolkit for Go](https://github.com/jackc/pgx)
- [pgx changelog](https://github.com/jackc/pgx/blob/master/CHANGELOG.md)
- [GO-2026-4772: memory-safety vulnerability before pgx v5.9.0](https://pkg.go.dev/vuln/GO-2026-4772)
- [GHSA-j88v-2chj-qfwx: SQL injection fixed in pgx v5.9.2](https://github.com/advisories/GHSA-j88v-2chj-qfwx)
- [sqlx](https://github.com/jmoiron/sqlx)
- [scany](https://github.com/georgysavva/scany)
