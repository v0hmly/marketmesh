# Базовый HTTP server

Пакет `httpserver` создаёт обычный `net/http.Server` с обязательными transport limits и безопасной цепочкой middleware. Он не вводит собственный router или web-фреймворк и не владеет listener либо TLS.

## Создание server

```go
healthHandler, err := httpserver.NewHealthHandler(health)
if err != nil {
	return err
}

server, err := httpserver.New(httpserver.Config{
	Handler:           healthHandler,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       15 * time.Second,
	WriteTimeout:      15 * time.Second,
	IdleTimeout:       60 * time.Second,
	RequestTimeout:    10 * time.Second,
	MaxHeaderBytes:    64 * 1024,
	MaxBodyBytes:      1024 * 1024,
	Logger:            log,
	Telemetry:         pipeline,
	Middleware: []httpserver.Middleware{
		authentication,
	},
})
if err != nil {
	return err
}

component, err := httpserver.Component("http", server, listener)
```

Все timeout и limits обязательны и должны быть положительными. `Handler`, `Logger`, `Telemetry` и каждый дополнительный middleware также обязательны при их использовании; typed nil отклоняется. `Listener`, TLS termination, routing, authentication и application handlers создаются composition root.

## Middleware и безопасность

Встроенная цепочка выполняется в следующем порядке:

1. проверка известного `Content-Length` и `http.MaxBytesHandler`;
2. максимальный request deadline с сохранением более короткого deadline и client cancellation;
3. извлечение W3C trace context, server span и histogram `http.server.request.duration`;
4. структурное завершение запроса;
5. panic recovery с общим `500 internal server error`;
6. дополнительные middleware в порядке конфигурации;
7. application handler.

Logs, spans и metrics содержат только нормализованный HTTP method, шаблон маршрута из `http.Request.Pattern`, status и duration. Фактический URL, query, request/response body, `Authorization`, `Cookie` и остальные headers не записываются. Неизвестный method становится `_OTHER`, а отсутствующий route template не заменяется URL path.

Запрос с известным `Content-Length` выше лимита отклоняется до handler статусом 413. Для streaming/chunked body чтение свыше лимита возвращает `*http.MaxBytesError`; application handler должен сопоставить эту ожидаемую ошибку с 413, если ответ ещё не записан.

## Health и shutdown

`NewHealthHandler` предоставляет `GET /livez` и `GET /readyz`. Readiness использует `runtime.Health`, но возвращает только `not ready`, не раскрывая имя или ошибку зависимости. Оба endpoint запрещают caching.

`Component` передаёт server общий shutdown deadline `runtime.Runner`. Сначала `http.Server.Shutdown` ждёт активные запросы; после deadline выполняется `http.Server.Close`, чтобы закрыть соединения и отменить request contexts. Hijacked connections остаются ответственностью их владельца согласно контракту стандартной библиотеки.

## Проверка

```bash
go test -race ./httpserver
```
