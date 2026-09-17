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

Периодическое перераспределение двух туннелей включено по умолчанию.
`TUNNEL_PERIODIC_REDISCOVERY_ENABLED=false` разрешён только при `ENVIRONMENT=test`
для фиксированной пары Gateway In в локальном контуре MM-64. Он отключает только
периодическое перераспределение: начальная проверка двух разных серверов,
устранение дублирующих соединений и восстановление после сетевых ошибок работают.
Этот режим не проверяет autoscaling или rolling deployment. Доступность при
перераспределении в production требует отдельного исправления MM-70.


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

## Подключение браузерного профиля User

`USER_BROWSER_ENABLED=true` заменяет fake E2E-маршруты настоящими GetMe и
UpdateMe на отдельных маршрутах 102/103. При выключенном флаге сохраняется
прежний fake-режим с маршрутами 100/101; их wire format не меняется. Значения
`INTERNAL_TARGET`, `INTERNAL_SERVER_NAME`, `EXPECTED_INTERNAL_URI` и
`INTERNAL_TLS_*` в реальном режиме задают mTLS-соединение с User; ожидаемая
URI должна указывать именно на workload User текущего окружения. Все `AUTH_*`
параметры соединения обязательны даже при `AUTH_BROWSER_ENABLED=false`.

Gateway Out передаёт закрытый BrowserContext в Auth ExchangeBrowserSession
с фиксированной audience `user`. Только Auth проверяет Origin и извлекает
cookie. Для каждого запроса выполняется новый обмен; assertion не кешируется
и не возвращается в туннель. В User отправляется только новая assertion:
унаследованные metadata, cookie и Authorization не используются. Прикладные
повторы обоих внутренних клиентов отключены; UpdateMe использует
`expected_version` без fake Idempotency-Key.

Закрытый ответ переносит только конечные признаки PROFILE_NOT_READY и
VERSION_CONFLICT. Первый признаётся только по точному ErrorInfo домена
`marketmesh.user`, второй — по Aborted у UpdateMe. Остальные ошибки проходят
обычную санитарную обработку туннеля. Произвольные details и внутренние
диагностические сообщения не пересылаются. Перед включением обновите Auth,
User и оба шлюза; откат выполняется согласованным выключением флага обоих
шлюзов перед возвратом бинарных файлов. Для отдельного E2E стенда
восстановите fake-конфигурацию `INTERNAL_*`. Смешанные режимы не согласуют
профильные маршруты, поэтому browser context не попадёт в FakeInternal.


## Адресная книга MM-68

`USER_ADDRESSES_BROWSER_ENABLED=true` требует `USER_BROWSER_ENABLED=true` и
включает отдельные маршруты 104–108: ListAddresses, CreateAddress, UpdateAddress,
DeleteAddress и SetDefaultAddress. Флаг по умолчанию выключен. В User требуется
миграция `000003_addresses`, `USER_ADDRESSES_ENABLED=true` и scopes
`user:addresses:read`/`user:addresses:write` в конфигурации аудитории Auth.

Включать следует после миграции и обновления User, затем gateway-out и gateway-in;
storefront показывает раздел только при сборке с `VITE_ACCOUNT_ADDRESSES_ENABLED=true`.
Новый gateway-in считает адресные маршруты частью readiness. Старый gateway-out
их не объявляет: смешанная поставка не отправляет адресные запросы в другой codec.
Запись не повторяется автоматически, адреса и cookie не попадают в metadata или логи.

Запрос каждой операции ограничен 16 КиБ, полный снимок до 20 адресов — 128 КиБ.
Только при включённом адресном флаге предел сообщения туннеля повышается до 128 КиБ;
размеры фрейма и окна остаются прежними. Ответ фрагментируется и учитывает flow control.
Ошибки ADDRESS_NOT_FOUND и ADDRESS_LIMIT_REACHED передаются только как точные
ErrorInfo домена `marketmesh.user` с ожидаемым кодом и без metadata; чужой адрес
неотличим от отсутствующего. CAS использует независимую версию книги.

Откат: сначала выключить раздел/маршруты, затем адресный флаг User. Старый код
профиля совместим с добавленными таблицей и колонкой; down-миграцию не выполнять
с сохранёнными адресами без отдельного решения о данных.

## Тема кабинета (MM-69)

`USER_SETTINGS_BROWSER_ENABLED=true` требует `USER_BROWSER_ENABLED=true`, но не
зависит от адресной книги. По умолчанию выключен. GetSettings/UpdateSettings
используют отдельные private Browser-конверты и маршруты 109/110, оба по 16 КиБ.
Gateway In считает режим готовым только при наличии обоих маршрутов. На каждом
запросе Gateway Out получает новый assertion Auth; User принимает только владельца
проверенной сессии и отдельные scopes `user:settings:read`/`user:settings:write`.
Мутации автоматически не повторяются. Публичны только PROFILE_NOT_READY и
VERSION_CONFLICT; темы строго ограничены system/light/dark.

Включение: применить добавочную `000004_settings`, обновить User и оба шлюза с
выключенными flags, добавить scopes в Auth, затем включить User
`USER_SETTINGS_ENABLED` и оба gateway flags; после их готовности собрать storefront
с `VITE_ACCOUNT_SETTINGS_ENABLED=true`. Старые декодеры не знают новых RouteId,
поэтому включение до обновления всех шлюзов недопустимо. Откат: скрыть настройку
в storefront, выключить flags шлюзов/User, вернуть бинарные файлы; колонки сохранять.
Down-миграция удаляет предпочтения и требует отдельного решения по данным.
