# Gateway In

Пограничный сервис в DMZ завершает ConnectRPC, WebSocket и SSE, применяет пограничные политики и принимает исходящий обратный туннель из внутренней зоны. Он не устанавливает соединения во внутреннюю зону.

## Сервер обратного туннеля

Пакет `internal/tunnel` реализует сервер `tunnel.v1.TunnelService/Connect` поверх двунаправленного gRPC-потока из MM-10. Сетевой listener и TLS-конфигурация остаются в точке сборки процесса; сервер необходимо регистрировать только на gRPC listener с обязательной взаимной TLS-аутентификацией.

Основные инварианты:

- gateway-in никогда не выбирает адрес, порт или gRPC-метод внутреннего сервиса: единственный селектор назначения — конечный `RouteId` из локального allowlist;
- сертификат gateway-out должен иметь проверенную цепочку, назначение `ClientAuth` и ровно один URI SAN, в точности совпадающий с локальным allowlist; `CommonName` не даёт полномочий;
- `Hello` только сужает локальную политику: capability, traffic class, route и лимит нельзя расширить объявлением peer;
- число handshaking и активных туннелей в сумме, число туннелей одного instance, запросы одного instance, запросы одного route и все очереди ограничены локальной конфигурацией; первый `Hello` обязан прийти до локального handshake timeout;
- `CONTROL_AUTH`, `REGULAR`, `REALTIME` и tunnel-control используют независимые bounded-очереди с round-robin обслуживанием, поэтому переполнение realtime не блокирует аутентификацию;
- `Data` передаётся только в пределах выданного byte-credit, сообщения и кадры ограничены negotiated limits, а последовательности кадров проверяются отдельно в каждом направлении;
- дедлайн логического запроса равен минимуму внешнего дедлайна и локального route limit; отмена, включая отмену во время ожидания credit, передаётся кадром `Cancel`;
- `HalfClose`, конечный `Result`, `Ping/Pong` и bounded `Drain` обрабатываются явным автоматом состояний;
- в публичные ошибки, структурированные логи, span attributes и metric labels не попадают payload, metadata, opaque identifiers, URI workload или внутренняя топология.

## Выбор туннеля и DC

Проверенная URI SAN identity статически сопоставляется с низкокардинальным
идентификатором DC через `PeerPolicy.DataCenterByURI`. Это сопоставление является
частью локальной политики gateway-in: peer не может объявить или изменить DC в
`Hello`, а отсутствие явной карты сохраняет однодоменный режим `local`.
Политика допускает не более 16 различных DC labels и проверяет их формат, поэтому
кардинальность телеметрии остаётся ограниченной конфигурацией.

Для каждого `RouteId` registry сначала выбирает DC, затем туннель внутри него.
DC балансируются smooth weighted round-robin, а туннели одного DC — стабильным
round-robin по opaque tunnel ID. Вернувшийся DC начинает с 10% веса и линейно
достигает полного веса за `FailbackWarmup` (по умолчанию 30 секунд), поэтому
failback не создаёт одновременный всплеск новых запросов. Если выбранный туннель
переполнен до отправки `Open`, допустим выбор следующего ready-туннеля. После
успешной постановки `Open` автоматического повтора нет: неопределённый результат
mutating-запроса возвращается вызывающей стороне без replay.

Туннель перестаёт быть eligible немедленно при `Drain` или разрыве. Даже если
transport ещё не сообщил о разрыве, отсутствие валидных входящих кадров в течение
`PingInterval + PongTimeout` переводит туннель в stale, закрывает его и исключает
из новых выборов; конфигурация ограничивает эту сумму пятью минутами. Все
состояния и очереди локальны конкретному `Registry`; общей
между процессами singleton-координации нет.

Метрики выбора и задержки содержат только статические `data_center`, конечный
`route` и конечный `status`. URI workload, instance/tunnel/request ID и payload в
labels не добавляются.

Для внутреннего health adapter и disposable E2E fault tooling доступен
`Registry.RoutingSnapshot(route)`. Это отсортированная defensive copy не более
чем из `MaxTunnels` записей со статическим DC, opaque tunnel/instance IDs,
конечным состоянием и числом активных запросов маршрута. Snapshot не двигает
курсор балансировки и не объявляет постоянную роль active/standby. Opaque IDs
разрешены только для краткоживущего сопоставления с pod в E2E ledger и запрещены
в logs, traces и metric labels. Публичный ответ front door должен использовать
только агрегированную route readiness и не сериализовать opaque IDs.

Пакет `internal/connectbridge` содержит ограниченный unary-адаптер ConnectRPC. Procedure и `RouteId` фиксируются при создании handler; произвольные HTTP headers, URL и method через туннель не передаются. Адаптер принимает и возвращает только типизированные protobuf-сообщения.

## Проверка

Из каталога `backend/services/gateway-in`:

```shell
go test ./...
go test -race -tags=integration ./...
go vet ./...
go build ./...
```

Интеграционные тесты поднимают in-memory gRPC listener с TLS 1.3 и обязательным клиентским сертификатом, выполняют настоящий ConnectRPC-вызов через обратный туннель и проверяют негативные mTLS/policy-сценарии, flow control, отмену, разрыв и Drain.


## Браузерный Auth через HTTPS

`AUTH_BROWSER_ENABLED` по умолчанию `false`. При включении обязательны
`PUBLIC_TLS_CERT_FILE` и `PUBLIC_TLS_KEY_FILE`: отдельный сертификат публичного
HTTPS listener на `HTTP_ADDRESS`, TLS 1.3. TLS завершается в Gateway In;
`Forwarded` и `X-Forwarded-Proto` не подтверждают защищённость запроса.
Health endpoints на этом listener также переходят на HTTPS. Listener туннеля
остаётся отдельным, с обязательным mTLS. Публичный сертификат не используется
как сертификат внутренней рабочей нагрузки.

Доступны только POST `/auth.v1.AuthService/RegisterCredentials`, `Login`,
`RefreshSession`, `Logout`, `LogoutAll`. Размещайте браузерное приложение на том
же HTTPS origin; CORS для отдельного origin не включён. Точное значение origin
должно присутствовать в `AUTH_ALLOWED_ORIGINS` сервиса Auth. Cookie остаются
`__Host-`, Secure, HttpOnly, SameSite=Strict, Path=/ без Domain. Gateway In
не интерпретирует их и не возвращает их в JSON/protobuf body.

На каждый маршрут действуют пределы 16 KiB для внутреннего запроса и ответа,
четыре одновременных вызова, общий срок `REQUEST_TIMEOUT` и независимая очередь
control/auth. Для Cookie ограничение 8192 байта, Origin — 2048,
Sec-Fetch-Site — 256; не более 16 отдельных строк каждого заголовка.
Дубликаты Origin сохраняются для отказа Auth. Все ответы этих методов имеют
`Cache-Control: no-store`; отдельные Set-Cookie передаются без объединения.

Readiness при включённом флаге требует всех пяти маршрутов. Недоступный туннель
или Auth приводит к безопасной ошибке; входящее соединение из DMZ к Auth
не создаётся. Существующие тестовые User-маршруты сохраняются до шага 06 MM-50.
Порядок обновления и отката описан в [протоколе туннеля](../../../docs/architecture/tunnel-protocol.md#публичные-браузерные-методы-auth-mm-50-шаг-05).

`task auth:browser:integration` проверяет HTTPS → reverse mTLS tunnel →
настоящий Auth с PostgreSQL primary/replica и Redis в изолированной Docker-сети.
Проверяются выдача двух cookie, ротация, отзыв одной/всех сессий и отказы
Origin/CSRF. Go cookie jar проверяет сетевой путь; отдельная проверка Chrome
подключается к тому же тесту через `AUTH_BROWSER_NODE_BIN` и
`AUTH_BROWSER_CHROME_BIN` при нативном запуске. Она проверяет недоступность
HttpOnly cookie из JavaScript и реальное поведение cookie на HTTPS origin.
Chrome использует новый временный профиль и доверяет только SPKI тестового
сертификата; пользовательский профиль не используется. Бинарные файлы Auth
и Gateway Out можно задать через `AUTH_BROWSER_AUTH_BIN` и
`AUTH_BROWSER_GATEWAY_OUT_BIN` (по умолчанию `/usr/local/bin/auth` и
`/usr/local/bin/gateway-out`).

## Публичный профиль User

`USER_BROWSER_ENABLED=true` включает HTTPS ConnectRPC методы
`/user.v1.UserService/GetMe` и `/user.v1.UserService/UpdateMe` через
отдельные маршруты tunnel 102/103. По умолчанию флаг выключен и сохраняется отдельный
FakeInternal E2E режим; при включении профиля FakeInternal endpoints не монтируются.
Режим несовместим с `E2E_ROUTING_SNAPSHOT_ENABLED=true`.
`PUBLIC_TLS_CERT_FILE` и `PUBLIC_TLS_KEY_FILE` обязательны при включённом Auth
или User. Readiness требует доступности обоих маршрутов профиля.

Gateway передаёт cookie непрозрачно в приватном контексте вместе с Origin и
Sec-Fetch-Site. Заголовки Authorization и клиентские assertions не используются.
Приватный `gateway.v1.UserBrowserService` недоступен браузеру. Ответы, включая
ошибки внешнего HTTP middleware, имеют `Cache-Control: no-store`; разрешён только
POST через TLS, запрос и ответ ограничены 16 KiB. UpdateMe использует версию
профиля для защиты от конкуренции, без transport retry и Idempotency-Key.
Состояние подготовки профиля возвращается как NotFound с типизированным
`ErrorInfo` (`marketmesh.user`, `PROFILE_NOT_READY`), конфликт версии — Aborted.
Произвольные сообщения и детали внутренних ошибок не выходят наружу.

Реальный режим разрешает только профильные маршруты 102/103, а legacy E2E —
100/101 с прежним wire format. Readiness требует пару маршрутов выбранного режима.
При несовпадении режимов шлюзов профильные маршруты не согласуются; browser
context не отправляется fake backend. Перед включением обновите обе стороны
туннеля до версии, поддерживающей новые RouteId.

`task auth:browser:integration` также запускает реальный User, его отдельную DB
и RW/RO роли, проверяет подготовку профиля, сохранение, конфликт версии,
изоляцию двух пользователей и отдельные отказы Auth/User. Бинарный файл User
можно задать через `USER_BROWSER_USER_BIN` (по умолчанию `/usr/local/bin/user`).
Тестовые профили явно создаются после проверки состояния подготовки; этот
стенд не заменяет `task user:registration:integration` для доставки событий.


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
