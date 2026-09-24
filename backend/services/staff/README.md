# Staff SSO

Минимальная серверная граница корпоративного портала MM-87. Реализует только SSO,
сессию и одноразовое приглашение, без бизнес-функций MM-59. Покупательская cookie,
Auth assertion и заголовок с клиентской идентичностью здесь не используются.

`internal/application` владеет идентичностью и портами IdP/хранилища; адаптеры
OIDC, PostgreSQL и HTTP не передают SQL/protobuf в use cases. Новая бизнес-модель
модерации в этой задаче не создаётся. `internal/app` выполняет ручную композицию,
валидацию конфигурации, TLS, тайм-ауты и graceful shutdown.

OIDC Authorization Code + PKCE S256, state/nonce и отдельная привязка к браузеру.
Проверка ID token — coreos/go-oidc: подпись RS256, issuer, audience, expiry, nonce,
подтверждённая почта. IdP и callback задаёт только сервер. Login challenge живёт
10 минут и атомарно потребляется до обмена кода. Явный повторный StartSso отзывает
старую сессию **до** cross-site перехода: Strict cookie не придёт на callback.

Сессия — случайные 256 бит в host-only Secure/HttpOnly/Strict cookie. PostgreSQL
хранит digest, проверяет idle и абсолютный deadline при каждом RPC. Login cookie
имеет Lax для callback и удаляется после него. Права связаны с `(issuer, subject)`;
приглашение требует ту же подтверждённую почту, атомарно потребляется и не меняет
права уже существующего участника. Ошибки SQL/IdP и секреты в ответы не попадают.
Аудит доступа содержит операцию, результат, digest сессии и проверенный principal;
почта, код, verifier, cookie и ссылка приглашения не логируются.

TLS требует доверенный корпоративный clientAuth-сертификат (`ClientCA`) до
обработки любого HTTP-маршрута. OIDC остаётся отдельной проверкой пользователя;
сертификат не назначает роли. Дополнительный IP allowlist проверяет реальный peer,
но не считается достаточным при SNAT.

RPC требуют точный Origin и POST; прямой callback требует state и browser binding.
Публичный frontdoor не проксирует staff. Сетевой периметр, cookies, CSP и локальный
IdP описаны в [ADR-0016](../../../docs/adr/0016-frontend-application-boundaries.md).

Команды запуска и браузера — [staff frontend](../../../frontend/apps/staff/README.md).
Проверки Go из `backend/`:

```sh
go test -race ./services/staff/...
STAFF_TEST_CONFIG=/path/to/local-staff-config.json go test -race -tags=integration ./services/staff/internal/adapter/out/postgres
```

Интеграционный тест использует явный тестовый DSN/config в сети общего PG,
создаёт случайно именованные строки и удаляет только их. Проверяет одноразовость
при конкурентном вызове, чужую почту, idle deadline и выдачу роли.

Миграция добавляет отдельную БД/схему staff в общий кластер, не меняет Auth/User.
Локальный provision применяет checksum ledger и минимальные DML grants. RW идёт
на primary по verify-full; replica и отдельная RO-роль сохраняются. Для отката
сначала остановить staff; `.down.sql` удаляет только staff и его данные, выполнять
его нужно лишь при явном отказе от этих данных. Откат приложения не требует down.
