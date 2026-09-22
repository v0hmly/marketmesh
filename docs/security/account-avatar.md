# Аватар аккаунта (MM-65)

Редактор на `/account/id` принимает PNG/JPEG до 5 МиБ. Браузер отправляет исходник
напрямую в quarantine Files, ожидает AV/CDR и две clean-копии, затем связывает
готовый файл с профилем. Показывается только производная PNG/JPEG до 20 МиБ:
клиент проверяет READY, MIME, точный размер и SHA-256 перед созданием Blob URL.
Исходник не используется даже для предварительного просмотра.

## Владелец и приватный API

`GetAvatar`, `SetAvatar`, `ClearAvatar` используют проверенную Auth-сессию и scopes
`user:profile:read/write`. Subject нельзя передать через браузерный запрос.
User хранит только `avatar_file_id` и независимую `avatar_version`; изменение
аватара не меняет версию личных данных. Запись требует `expected_version`.

Перед SetAvatar User обращается к `FileAvatarService.InspectOwnedAvatar` на
отдельном mTLS listener. Files допускает только workload User с точными trust
domain, environment, cluster, namespace и service account. User проверяет такой
же scope сервера Files и DNS SAN. На обычном gateway listener этот сервис не
зарегистрирован. Приватный listener не принимает cookie, authorization или
session assertion и не выдаёт URL либо байты файлов. Он доверяет принципалу,
которого User проверил через Auth; это привилегированная межсервисная граница.
Tenant пока равен subject согласно ADR-0015. Audit Inspect содержит subject,
session ID и workload; фоновое удаление обозначено `actor_kind=workload` без
выдуманной пользовательской сессии.

## Запись и очистка

Миграция User `000006_avatar` добавляет ссылку, версию и `users.avatar_retirements`.
Под блокировкой строки профиля CAS и запись прежнего файла в очередь выполняются
одной транзакцией. RPC Files находится вне транзакции. Старый файл становится
недоступен для повторной привязки сразу после commit, даже до исполнения очереди.
Записи завершённых удалений сохраняются: удалять их отдельно от профиля нельзя,
иначе появится гонка повторной привязки и фонового удаления.

Worker вызывает идемпотентный `RetireOwnedAvatar`; Files фиксирует tombstone,
а его собственный worker очищает объекты. User использует lease 30 секунд,
уникальный token, RPC deadline 5 секунд и backoff 2–256 секунд. Устаревший worker
не может завершить чужую lease. Недоступный Files не блокирует ClearAvatar или
остальной профиль: удаление продолжится после восстановления зависимости.
В диагностике только класс ошибки и случайный file ID, без сырых ошибок/URL.

До привязки кандидат остаётся обычным принадлежащим пользователю ресурсом Files.
Его можно явно отменить в редакторе. Если закрыть вкладку после READY, но до
SetAvatar, готовый непривязанный файл не удаляется автоматически. Незавершённая
загрузка ограничена сроком Files; состояние кандидата между вкладками и reload
не сохраняется. Удаление всего аккаунта и его файлов реализуется в MM-92.

## Браузерная граница

Capability URL и выбранный файл живут только в памяти вкладки. Прямые PUT/GET
допускают точные HTTPS origins, ограниченный набор подписанных заголовков,
`credentials: omit`, `no-referrer`, запрет redirect и deadline 30 секунд.
URL, cookie, изображения и имена файлов не записываются в Web Storage/telemetry.
Blob URL освобождается при замене, выходе, смене владельца и unmount.
CSP разрешает `blob:` только для изображений и конкретные Files origins для fetch;
CORS не разрешает wildcard или credentials.

После неоднозначной записи UI требует перечитать состояние; мутация автоматически
не повторяется. Восстановление истёкшей сессии допускается только для чтения,
после освобождения shared lock и с проверкой прежних subject/generation.
Конфликт другой вкладки не перезаписывается молча.

## Поставка и откат

1. Применить добавочную миграцию 000006, сохранить RW/RO grants и дождаться реплики.
2. Обновить Files и включить `Control.Avatar.Enabled`, задать отдельный `Address`
   и точный `User` scope. Не публиковать этот порт за gateway/frontdoor.
3. Обновить все User; `USER_AVATAR_ENABLED=true` требует `USER_PROFILE_ENABLED`.
   Задать `USER_FILES_TARGET`, `USER_FILES_SERVER_NAME`, абсолютные пути
   `USER_FILES_TLS_CERT_FILE`, `USER_FILES_TLS_KEY_FILE`, `USER_FILES_TLS_CA_FILE`,
   `USER_FILES_OWN_URI` и `USER_FILES_EXPECTED_URI`.
4. Обновить оба шлюза с `USER_AVATAR_BROWSER_ENABLED=true` и прежними User/Files
   browser-флагами. Затем собрать storefront с `VITE_ACCOUNT_AVATAR_ENABLED=true`,
   `VITE_FILES_UPLOAD_ORIGIN` и `VITE_FILES_DOWNLOAD_ORIGINS` (через запятую).
   Настроить точные CSP/CORS origins; настроек одного frontend недостаточно.

В локальном dev всё включается командой [files.py up](../../infra/account-local/README.md).
Флаги нужны для порядка поставки и совместимости конфигурации. Откат — убрать
редактор и новые публичные маршруты, сохранив таблицы и работающий cleanup worker
до опустошения очереди. Откат приложения не требует down-миграции; она удаляет
метаданные и допустима только для одноразовой тестовой БД. Не удалять volumes,
базу Files или KMS ключи. Известные инфраструктурные CVE остаются в действующем
[исключении Files](files-dev-acceptance.md), MM-65 не объявляет их исправленными.

## Проверки

PostgreSQL integration проверяет атомарность CAS/очереди, конкуренцию 16 writers,
изоляцию владельцев, независимость версий, lease takeover и запрет reattach после
retirement. mTLS-тесты выполняют настоящие handshakes с разрешёнными и чужими
scope/CA и проверяют разделение RPC listener. Frontend проверяет capabilities,
неизвестный исход, восстановление сессии и очистку Blob. Полный локальный E2E
соединяет Mailpit/Auth/User/gateway/Files/storage/sandbox и настоящий Chromium.
Тяжёлый сценарий выполняется в OrbStack; новые jobs GitHub Actions не добавляются.
