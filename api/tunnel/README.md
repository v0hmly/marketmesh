# Валидация протокола туннеля

Модуль содержит общий fail-closed декодер кадров обратного туннеля. Он используется обеими сторонами границы доверия до изменения состояния потока, постановки запроса в очередь или вызова внутреннего gRPC-сервиса.

```go
frame, err := tunnelv1.DecodeGatewayInFrame(data)
if err != nil {
	// Нарушение протокола обрабатывается без вывода data в error или log.
	return err
}
```

`DecodeGatewayOutFrame` принимает только `ConnectRequest`, допустимый в направлении internal → DMZ. `DecodeGatewayInFrame` принимает только `ConnectResponse`, допустимый в направлении DMZ → internal. Декодеры до protobuf unmarshal ограничивают размер wire-кадра, требуют ровно один header и один oneof payload, затем отклоняют неизвестные поля и проверяют все структурные пределы v1.

Пакет намеренно не реализует:

- состояние handshake и монотонность sequence;
- проверку согласованных capability, route и фактических лимитов конкретного туннеля;
- сравнение deadline с текущим временем и максимумом маршрута;
- внутреннее отображение RouteId на сервис и gRPC-метод;
- авторизацию и проверку session assertion.

Эти проверки зависят от состояния и локальной политики, поэтому `gateway-in` и `gateway-out` применяют их независимо после общего структурного декодирования.

## Проверка

```bash
go test ./...
go test -race ./...
go test -run '^$' -fuzz=FuzzDecodeGatewayInFrame -fuzztime=10s ./v1
go test -run '^$' -fuzz=FuzzDecodeGatewayOutFrame -fuzztime=10s ./v1
```
