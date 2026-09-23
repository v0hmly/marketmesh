# Logger

`logger` — типизированная обёртка над `zerolog` для сервисов MarketMesh. Она предоставляет единый API, корреляцию с OpenTelemetry, маскирование структурных полей и адаптер для `log/slog`.

## Создание и внедрение

```go
log, err := logger.New(logger.Config{
    Service:     "auth",
    Version:     version,
    Environment: environment,
    Level:       "info",
})
if err != nil {
    return fmt.Errorf("create logger: %w", err)
}

// Logger передаётся компонентам через конструкторы.
handler := NewHandler(log)
```

В JSON-событие всегда добавляются поля `service`, `version`, `environment` и `time`. Уровень по умолчанию — `info`. Настроенный уровень относится только к созданному экземпляру и не влияет на остальные логгеры процесса.

Для локальной разработки можно включить человекочитаемый вывод:

```go
log, err := logger.New(logger.Config{
    Service:     "auth",
    Version:     version,
    Environment: "local",
    Console:     true,
})
```

`ConsoleWriter` разрешён только для окружения `local`. Во всех остальных окружениях используется структурированный JSON.

## Запись событий

Для каждого уровня доступны обычный и контекстный варианты методов: `Trace`/`TraceContext`, `Debug`/`DebugContext`, `Info`/`InfoContext`, `Warn`/`WarnContext`, `Error`/`ErrorContext`, `Fatal`/`FatalContext`, `Panic`/`PanicContext`.

```go
log.Info(
    "user authenticated",
    logger.String("user_id", userID),
    logger.Bool("mfa", true),
)

log.ErrorContext(
    ctx,
    "request failed",
    logger.Err(err),
    logger.Duration("duration", duration),
)
```

Поддерживаются типизированные поля `String`, `Bool`, `Int`, `Int64`, `Uint64`, `Float64`, `Duration`, `Time`, `Bytes`, `Err`, `NamedError`, `JSON` и `Sensitive`.

Повторяющиеся поля можно один раз привязать к дочернему логгеру. Родительский экземпляр при этом не изменяется:

```go
requestLog := log.With(
    logger.String("component", "http"),
    logger.String("request_id", requestID),
)
requestLog.InfoContext(ctx, "request received")
```

Перед дорогим вычислением поля можно проверить уровень без создания события:

```go
if log.Enabled(logger.LevelDebug) {
    log.Debug("cache snapshot", logger.String("summary", buildSummary()))
}
```

Для редких типов доступен явно обозначенный `UnsafeAny`. Он использует рефлексию и может раскрыть вложенные чувствительные данные, поэтому для контрактов сервера и клиента предпочтительнее типизированные поля.

## JSON без двойного кодирования

`JSON` принимает `json.RawMessage`, проверяет корректность и сохраняет защитную копию. В результате объект или массив встраивается в событие как JSON, а не как экранированная строка:

```go
log.DebugContext(
    ctx,
    "response received",
    logger.JSON("payload", json.RawMessage(body)),
)
// "payload":{"request_id":"request-42"}, а не
// "payload":"{\"request_id\":\"request-42\"}"
```

Невалидный JSON заменяется строкой `[INVALID JSON]`, чтобы логирование не влияло на обработку запроса и не повреждало всё JSON-событие. `slog.Any` с `json.RawMessage` использует те же правила.

Полный request/response body обычно содержит лишние или чувствительные данные. Для логов серверного и клиентского транспорта используйте только ограниченную по размеру и явно разрешённую структуру, предпочтительно на уровне `debug`. `JSON` не маскирует вложенные ключи внутри документа.

## Корреляция с трассировкой

Контекстные методы извлекают из `context.Context` валидный OpenTelemetry `SpanContext` и добавляют `trace_id` и `span_id`:

```go
log.InfoContext(ctx, "request completed", logger.String("method", "POST"))
```

Обычные методы не читают контекст и не несут накладных расходов OpenTelemetry. Произвольные значения из `context.Context` в событие не копируются.

## Маскирование

Значения чувствительных структурных полей заменяются до передачи в `zerolog`, поэтому конвейер не разбирает и не пересобирает готовый JSON. По умолчанию маскируются распространённые ключи паролей, токенов, авторизационных заголовков, cookies, платёжных и персональных данных.

```go
log.Info("credentials received", logger.String("token", token))
// "token":"[REDACTED]"
```

Если чувствительность определяется смыслом значения, а не стандартным именем ключа, используйте `Sensitive`. Значение такого поля вообще не сохраняется в `Field` и всегда заменяется маской:

```go
log.Info("credential received", logger.Sensitive("credential", credential))
```

Дополнительные ключи и значение замены задаются при создании:

```go
log, err := logger.New(logger.Config{
    Service:     "auth",
    Version:     version,
    Environment: environment,
    MaskFields:  []string{"private_note"},
    MaskValue:   "***",
})
```

Сопоставление ключей точное. В собственных событиях следует использовать канонические имена `lower_snake_case`; для стандартных HTTP-заголовков предусмотрены распространённые варианты регистра. Маскирование применяется к `Field` и атрибутам `slog`, включая вложенные `slog.Group`.

Маскирование по имени поля не является DLP: оно не ищет секреты внутри текста сообщения, строки ошибки или объекта под нейтральным ключом. Не передавайте в лог целиком запросы, ответы, заголовки, доменные объекты и произвольный пользовательский ввод.

Системные ключи `service`, `version`, `environment`, `time`, `level`, `message`, `trace_id` и `span_id` нельзя подменить пользовательским полем. Конфликтующее поле автоматически записывается с префиксом `fields.`, например `fields.service`.

## slog и net/http

`Slog` позволяет направить библиотеки, использующие стандартный `log/slog`, в тот же конвейер:

```go
slog.SetDefault(log.Slog())
```

Для поля `http.Server.ErrorLog`, которое принимает `*log.Logger`, используется отдельный адаптер:

```go
server := &http.Server{
    Addr:     ":8080",
    Handler:  handler,
    ErrorLog: log.HTTPErrorLog(),
}
```

Slog-группы записываются плоскими ключами, например `request.method`. Это сохраняет однозначность имён и не требует промежуточных объектов.

## Производительность

Типизированный путь не использует `map[string]any`, промежуточный JSON или рефлексию. В пакете есть сравнение прямого `zerolog`, обёртки, контекстного вызова, маскирования и slog-адаптера:

```bash
go test -run '^$' -bench '^BenchmarkLogger$' -benchmem ./logger
```

Для обычного типизированного вызова, `Enabled`, заранее привязанных полей и маскирования целевой контракт — `0 B/op` и `0 allocs/op`. Создание JSON-поля выполняет валидацию и защитное копирование один раз. Конкретное время зависит от процессора и writer.

## Безопасность и обработка ошибок

- Не записывайте токены, cookies, пароли, секреты, платёжные данные и персональные данные под нейтральными ключами или внутри сообщения.
- Выбирайте безопасные структурные поля явно и предпочитайте типизированные конструкторы вместо `UnsafeAny`.
- Ошибка логируется один раз на том слое, где она окончательно обрабатывается. Если функция возвращает ошибку вызывающему коду, она не должна одновременно логировать её.
- Не используйте пакетный singleton. Создавайте `Logger` в composition root сервиса и передавайте его зависимостям явно.
