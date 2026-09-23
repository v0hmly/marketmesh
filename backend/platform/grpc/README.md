# Базовые gRPC client и server

Пакет `grpc` собирает безопасные transport-настройки внутренних Go-сервисов MarketMesh поверх `google.golang.org/grpc`. Он реализует bounded lifecycle gRPC server и интегрируется с `backend/platform/runtime` через общий `Component`, использует `backend/platform/logger` для структурных событий и `backend/platform/telemetry` для OpenTelemetry traces/metrics без process-wide globals.

Пакет не скрывает обычные `*grpc.Server` и `*grpc.ClientConn`, не регистрирует доменные protobuf services и не преобразует transport DTO в application-команды. Эти обязанности остаются в `backend/services/<service>/internal/adapter` и `internal/app`.

## Server

```go
server, err := platformgrpc.NewServer(platformgrpc.ServerConfig{
	Environment:            "production",
	ConnectionTimeout:      5 * time.Second,
	RequestTimeout:         10 * time.Second,
	KeepaliveTime:          30 * time.Second,
	KeepaliveTimeout:       10 * time.Second,
	MaxReceiveMessageBytes: 4 << 20,
	MaxSendMessageBytes:    4 << 20,
	Security: platformgrpc.ServerSecurity{
		TLSConfig:                tlsConfig,
		RequireClientCertificate: true,
	},
	Logger:          log,
	Telemetry:       pipeline,
	ErrorCodeMapper: mapApplicationError,
	UnaryAuthentication: unaryAuthentication,
	StreamAuthentication: streamAuthentication,
})
if err != nil {
	return err
}

userpb.RegisterUserServiceServer(server.GRPCServer(), userHandler)
component, err := server.Component("grpc", listener)
```

`Component` переводит отдельные standard gRPC health services `marketmesh.health.v1.Liveness` и `marketmesh.health.v1.Readiness` в `SERVING` при старте. Пустое standard service name зеркалирует readiness для обычных gRPC load balancers. При остановке readiness снимается до `GracefulStop`; по истечении переданного deadline выполняется принудительный `Stop`, который отменяет contexts активных RPC.

Reflection регистрируется только при `EnableReflection: true`. Production-конфигурация с reflection отклоняется конструктором.

Встроенная цепочка server interceptors выполняет recovery, безопасное структурное логирование и status mapping. Затем выполняются явно переданные authentication и дополнительные interceptors. Логи содержат только полное имя метода, итоговый gRPC code и duration: request/response bodies и metadata не журналируются. Panic не завершает процесс и возвращается клиенту как `Internal` без исходного значения.

`ErrorCodeMapper` возвращает только `codes.Code`, а публичный текст выбирает пакет. Поэтому текст доменной ошибки, SQL, внутренний адрес или секрет не могут случайно попасть в `status.Message`. Неизвестные ошибки и `codes.Unknown` становятся `Internal`.

По умолчанию все error details также удаляются. `ServerConfig.PublicErrorInfo` позволяет composition root явно разрешить фиксированный `google.rpc.ErrorInfo` для точного сочетания полного RPC method, status code, domain и reason. При совпадении пакет создаёт новый detail из конфигурации; metadata, неизвестные поля, отладочные details и исходное сообщение не переносятся. Это сохраняет машинные состояния вроде подготовки профиля User без раскрытия внутренних ошибок. Policy содержит не более 32 элементов, копируется при создании сервера и применяется к unary/stream одинаково; значения не берутся из запроса.

## Client

```go
client, err := platformgrpc.NewClient(ctx, platformgrpc.ClientConfig{
	Target:                 "dns:///user.internal:8443",
	Environment:            "production",
	ConnectTimeout:         5 * time.Second,
	CallTimeout:            3 * time.Second,
	KeepaliveTime:          30 * time.Second,
	KeepaliveTimeout:       10 * time.Second,
	MaxReceiveMessageBytes: 4 << 20,
	MaxSendMessageBytes:    4 << 20,
	Security: platformgrpc.ClientSecurity{
		TLSConfig:                tlsConfig,
		RequireClientCertificate: true,
	},
	Logger:               log,
	Telemetry:            pipeline,
	UnaryAuthentication: outboundUnaryAuthentication,
	StreamAuthentication: outboundStreamAuthentication,
})
if err != nil {
	return err
}
defer client.Close()

userClient := userpb.NewUserServiceClient(client.Connection())
```

`NewClient` создаёт одно переиспользуемое connection и не возвращается до состояния `Ready` либо `ConnectTimeout`. Каждый unary RPC и весь stream получают не более `CallTimeout`; более короткий deadline вызывающего context сохраняется. Cancellation передаётся transport и серверному handler.

Повторы interceptor платформы выключены по умолчанию. Для мутаций задавайте `ClientConfig.DisableRetries: true`: режим запрещает одновременно задавать `Retry`, отключает native gRPC retry policy и принятие service config от resolver. Это исключает повтор уже обработанной операции по resolver retry policy. Прозрачные транспортные повторы gRPC, когда запрос не отправлен либо сервером ещё не обработан, сохраняются ([контракт gRPC](https://pkg.go.dev/google.golang.org/grpc#WithDisableRetry)). Значение `false` сохраняет прежнее поведение клиента, включая принятие resolver service config. `RetryPolicy` разрешает не более пяти попыток только для явно перечисленных полных имён идемпотентных unary methods и только для `Unavailable`, `ResourceExhausted` или `Aborted`. Общий `CallTimeout` ограничивает все попытки вместе. Streaming RPC автоматически не повторяются.

## TLS и mTLS

TLS configuration клонируется, `InsecureSkipVerify` запрещён, минимальная версия по умолчанию — TLS 1.2. При `RequireClientCertificate` server требует client CA и проверяет сертификат клиента, а client требует собственный certificate или callback ротации.

Plaintext никогда не включается неявно:

- `PlaintextLocal` предназначен только для loopback, `bufconn` и непроизводственных окружений;
- `PlaintextTrustedMesh` является явным production-исключением, допустимым только когда согласованный service mesh обеспечивает шифрование и workload identity вне процесса;
- `PlaintextForbidden` требует TLS и используется по умолчанию.

Authentication остаётся отдельным механизмом: валидный mTLS certificate не заменяет policy «кто может вызвать какой method». Authentication interceptors должны добавлять или проверять только необходимую metadata и не передавать токены в logs, errors, traces или metrics.

## Проверка

Unit- и `bufconn` integration tests покрывают TLS policy, unary/stream metadata, deadlines, cancellation, safe status mapping, panic recovery, retry, connection reuse, health и bounded shutdown:

```bash
go test -race ./grpc
```
