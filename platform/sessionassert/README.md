# sessionassert

Платформенная библиотека подписанных внутренних session assertions по [ADR-0005](../../docs/adr/0005-user-session-and-identity-propagation.md).

Сервис аутентификации асимметрично (Ed25519) подписывает короткоживущее утверждение о пользовательской сессии для конкретной аудитории; доменный сервис проверяет его локально по набору открытых ключей, не вызывая сервис аутентификации.

## Свойства

- Формат — JWS compact serialization, алгоритм зафиксирован: EdDSA/Ed25519; значение `alg` из заголовка игнорируется (RFC 8725).
- Claims: `iss`, `aud` (одиночная строка), `sub`, `sid`, `iat`, `exp`, `jti`, `auth_time`, `acr`, `amr`, `scope`, `act` (опционально), `typ`. Без email, имени и исходного токена.
- Верификатор проверяет тип, подпись по `kid` из доверенного набора, издателя, аудиторию, срок действия с ограниченным leeway (по умолчанию 30 с) и обязательные области действия.
- `StaticKeySet` — потокобезопасный набор ключей с ротацией в перекрытие; неизвестный `kid` не вызывает сетевой загрузки.
- Только стандартная библиотека, новых зависимостей нет.

## Пример

```go
iss, _ := sessionassert.NewIssuer(priv, "k-2026-09", "auth.marketmesh")
token, err := iss.Issue(sessionassert.IssueParams{
	Audience:  "user-service",
	Subject:   "u-123",
	SessionID: "s-456",
	TTL:       2 * time.Minute,
	AuthTime:  authTime,
	ACR:       "urn:marketmesh:acr:1",
	AMR:       []string{"pwd", "otp"},
	Scopes:    []string{"profile:read"},
})

keys := sessionassert.NewStaticKeySet()
_ = keys.Add("k-2026-09", pub)
v, _ := sessionassert.NewVerifier("auth.marketmesh", "user-service", keys,
	sessionassert.RequireScopes("profile:read"))
claims, err := v.Verify(token)
```
