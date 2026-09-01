# Protobuf-схемы

Будущие схемы группируются по границе и версии:

```text
public/<area>/v1/    публичные ConnectRPC-контракты
internal/<area>/v1/  внутренние gRPC-контракты
tunnel/v1/           протокол обратного туннеля
```

Отображение публичного маршрута во внутренний метод должно оставаться явным и версионируемым согласно ADR-0003.

Этот каталог — единственный источник продуктовых protobuf-схем. Новый файл обязан содержать версионированный `package`, полный `go_package` внутри модуля `github.com/v0hmly/marketmesh/api/gen/go`, комментарии для API-элементов и значение `*_UNSPECIFIED = 0` для каждого enum.

Сначала изменяется схема, затем выполняются `task api:lint`, `task api:breaking` и `task api:generate`. Файлы в `api/gen/go` и `api/gen/ts` являются результатом генерации и вручную не исправляются.

## Обратный туннель v1

`tunnel/v1/tunnel.proto` задаёт bidirectional gRPC stream `TunnelService.Connect`. Направления намеренно используют разные сообщения: `ConnectRequest` от `gateway-out` и `ConnectResponse` от `gateway-in`. Единственным селектором назначения является enum `RouteId`; строковых host, port, URL и полного имени gRPC-метода в схеме нет.

Wire-правила v1 строгие: неизвестные frame types, поля и значения enum отклоняются общим пакетом `api/tunnel/v1`. Новое поле, которое protobuf breaking check считает совместимым, нельзя использовать без согласованной capability или новой версии протокола.
