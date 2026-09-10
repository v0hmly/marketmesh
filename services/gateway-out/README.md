# Gateway Out

Внутренний ретранслятор устанавливает исходящий двунаправленный gRPC-туннель к Gateway In и передаёт разрешённые запросы внутренним gRPC-сервисам. Он независимо проверяет маршрут, метаданные, дедлайн и размер запроса.

## Реализация reverse tunnel

Пакет `internal/tunnel` реализует internal-сторону протокола MM-10:

- исходящий gRPC transport использует только mTLS, проверяет DNS-имя и ровно одну ожидаемую URI workload identity Gateway In;
- строгий codec сохраняет fail-closed wire-проверки MM-10 до изменения состояния tunnel;
- локальный неизменяемый registry отображает `RouteId` на заранее заданные gRPC client, полное имя метода и protobuf DTO factories;
- target, host, port и имя внутреннего метода отсутствуют в tunnel frames и не могут быть выбраны стороной DMZ;
- отдельные gRPC clients, очереди, concurrency limits и receive windows задаются для control/auth, regular и realtime классов;
- unary request собирается только в пределах route и negotiated limits, а response отправляется только в рамках credit Gateway In;
- deadline, cancellation, W3C trace context и внутренний session assertion распространяются к внутреннему RPC; произвольная metadata и внутренние trailers не переносятся;
- reconnect выполняется одним последовательным loop с ограниченными attempts, exponential backoff, jitter и верхней границей;
- transport keepalive дополнен прикладными `Ping`/`Pong`, а shutdown выполняет ограниченный `Drain` и отменяет оставшиеся RPC по deadline;
- внутренние ошибки преобразуются только в конечный `ResultCode`; bodies, assertions, bearer tokens, request IDs и тексты ошибок не попадают в logs, spans или metric attributes.

Realtime routes требуют отдельного типизированного streaming adapter и до его регистрации отклоняются fail-closed. Это не позволяет ошибочно обработать realtime route как unary RPC. При этом realtime queue/client/limits уже изолированы от control/auth и regular классов.

`Client.Component` подключает tunnel к `platform/runtime`. Gateway Out не создаёт входящий listener в DMZ.


## Подключение браузерных методов Auth

`AUTH_BROWSER_ENABLED=false` сохраняет прежний набор маршрутов. Для включения
нужны `AUTH_TARGET`, `AUTH_SERVER_NAME`, `EXPECTED_AUTH_URI`,
`AUTH_TLS_CERT_FILE`, `AUTH_TLS_KEY_FILE`, `AUTH_TLS_ROOT_CA_FILE`.
Это фиксированное внутреннее соединение с Auth: проверяются CA, DNS-имя,
ровно одна ожидаемая URI сервера и клиентский сертификат Gateway Out.
Цепочка доверия Auth должна разрешать эту рабочую нагрузку gateway-out
в том же окружении. Auth должен работать с `AUTH_SESSIONS_ENABLED=true`.

Пять браузерных маршрутов control/auth используют отдельный gRPC client
и типизированные DTO `AuthBrowserService`. Cookie и Set-Cookie находятся
только в закрытых DTO конкретного маршрута; generic metadata не расширяется.
Размеры ограничены 16 KiB, срок — `CALL_TIMEOUT`. Прикладные повторы и retry
policy из resolver service config явно отключены через
`platform/grpc.ClientConfig.DisableRetries`; прозрачный повтор транспорта
возможен только для ещё не обработанного сервером запроса. Другие маршруты
используют прежнее соединение `INTERNAL_*`.

Обновите Auth и оба шлюза до включения флага; старый строгий декодер отвергает
новый RouteId 6. Для отката сначала выключите флаг на обоих шлюзах.
[Полный порядок и границы контракта](../../docs/architecture/tunnel-protocol.md#публичные-браузерные-методы-auth-mm-50-шаг-05).
