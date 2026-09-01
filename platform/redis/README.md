# Redis

Пакет `platform/redis` создаёт один независимый Redis client для одной trust zone. Edge и Auth обязаны использовать разные экземпляры, адреса, credentials и lifecycle-компоненты; глобального singleton нет.

## Выбор клиента

Используется [`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis) версии `v9.22.0`:

- официальный Go-клиент Redis под лицензией BSD-2-Clause;
- активная поддержка и заявленная совместимость с Redis 8.10, используемым в MM-9;
- типизированный API команд, RESP3, bounded pool и поддержка `context.Context`;
- upstream module graph ограничен `xxhash` (MIT), `xxh3` (BSD-2-Clause), `cpuid` (MIT), `go.uber.org/atomic` (MIT) и `x/sys` (BSD-3-Clause); Ginkgo/Gomega (MIT) используются upstream только для тестов;
- фактически собираемый `platform/redis` добавляет к уже существующему platform-графу runtime-пути `go-redis` и `go.uber.org/atomic`; это подтверждается `go list -deps ./redis`;
- версия включает исправления прежней утечки credentials в tracing, races и ошибок parser; `govulncheck` остаётся обязательной проверкой каждого изменения.

Экспериментальные client-side caching и auto-pipelining не включены. `redisotel` намеренно не используется: библиотека MarketMesh создаёт собственные spans и metrics без сериализации команд, ключей и значений.

## Гарантии

- адрес, username, password и TLS server name передаются как `runtime.Secret`;
- TLS 1.2+ обязателен для production; plaintext разрешается только с явным `PlaintextException` для документированной защищённой сети;
- адрес скрыт от внутренних ошибок клиента через собственный dialer и redacted `net.Conn`;
- pool, connect, command, socket I/O, readiness и shutdown имеют конечные пределы;
- встроенные command retries `go-redis` отключены;
- `Execute` всегда делает одну попытку и применяется для записей и операций с недоказанной идемпотентностью;
- `ExecuteIdempotent` повторяет только transient failures и только если вызывающая сторона доказала идемпотентность всей callback-операции;
- callback получает operation context с конечным deadline;
- при cancellation API возвращается немедленно; уже отправленную Redis-команду нельзя отменить на сервере, но её локальное завершение ограничено `Command` timeout;
- traces и metrics содержат только `role`, вид операции, результат и фиксированную причину retry; команды, ключи, значения, адреса, credentials и тексты ошибок не записываются;
- readiness и закрытие подключаются к `platform/runtime`.

`Operation` не должна сохранять переданный `Cmdable`, запускать через него фоновые команды или подменять переданный context. Cancellation записи не доказывает, что Redis не применил её, поэтому такой результат нельзя автоматически повторять. Пакет не определяет key conventions, cache/repository abstractions, session lifecycle, locks, rate limits, Lua или доменные команды.

## Пример

```go
client, err := redis.New(ctx, config, telemetryPipeline)
if err != nil {
	return err
}

err = client.Execute(ctx, func(ctx context.Context, commands goredis.Cmdable) error {
	return commands.Set(ctx, "service-owned:key", value, time.Minute).Err()
})

err = client.ExecuteIdempotent(ctx, func(ctx context.Context, commands goredis.Cmdable) error {
	return commands.Get(ctx, "service-owned:key").Err()
})
```

Для локальных integration-тестов MM-9 используется исключение `MM-9 isolated internal Docker network`; оно не является разрешением отключать TLS в production.

## Проверка

```bash
GOWORK=off go test -race ./redis
task redis:integration
```
