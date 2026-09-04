package sessionassert

import (
	"fmt"
	"time"
)

// TokenType — обязательное значение typ заголовка и claims утверждения.
const TokenType = "mm-session-assertion+jwt"

// Claims — типизированное содержимое проверенного утверждения.
// По требованию приватности ADR-0005 здесь нет email, имени и исходного
// внешнего токена: только псевдонимные идентификаторы и признаки
// аутентификации.
type Claims struct {
	Issuer    string    // iss — издатель (сервис аутентификации)
	Audience  string    // aud — единственная целевая аудитория
	Subject   string    // sub — неизменяемый идентификатор субъекта
	SessionID string    // sid — идентификатор сессии
	IssuedAt  time.Time // iat — время выпуска
	ExpiresAt time.Time // exp — время истечения
	ID        string    // jti — идентификатор утверждения
	AuthTime  time.Time // auth_time — время первичной аутентификации
	ACR       string    // acr — уровень аутентификации
	AMR       []string  // amr — методы аутентификации
	Scopes    []string  // scope — ограниченные области действия
	Actor     string    // act — субъект делегирования, опционально
}

// String возвращает краткое описание утверждения для журналов. Сырой токен
// и подпись не выводятся.
func (c *Claims) String() string {
	return fmt.Sprintf("sessionassert.Claims{iss:%q aud:%q sub:%q sid:%q jti:%q exp:%s}",
		c.Issuer, c.Audience, c.Subject, c.SessionID, c.ID, c.ExpiresAt.UTC().Format(time.RFC3339))
}

// HasScope сообщает, содержит ли утверждение указанную область действия.
func (c *Claims) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// claimsJSON — wire-представление claims. NumericDate кодируются секундами
// unix, аудитория — одиночной строкой.
type claimsJSON struct {
	Issuer    string   `json:"iss"`
	Audience  string   `json:"aud"`
	Subject   string   `json:"sub"`
	SessionID string   `json:"sid"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	ID        string   `json:"jti"`
	AuthTime  int64    `json:"auth_time"`
	ACR       string   `json:"acr"`
	AMR       []string `json:"amr,omitempty"`
	Scopes    []string `json:"scope,omitempty"`
	Actor     string   `json:"act,omitempty"`
	Type      string   `json:"typ"`
}

func (c *Claims) toJSON() claimsJSON {
	return claimsJSON{
		Issuer:    c.Issuer,
		Audience:  c.Audience,
		Subject:   c.Subject,
		SessionID: c.SessionID,
		IssuedAt:  c.IssuedAt.Unix(),
		ExpiresAt: c.ExpiresAt.Unix(),
		ID:        c.ID,
		AuthTime:  c.AuthTime.Unix(),
		ACR:       c.ACR,
		AMR:       c.AMR,
		Scopes:    c.Scopes,
		Actor:     c.Actor,
		Type:      TokenType,
	}
}

func claimsFromJSON(j claimsJSON) *Claims {
	return &Claims{
		Issuer:    j.Issuer,
		Audience:  j.Audience,
		Subject:   j.Subject,
		SessionID: j.SessionID,
		IssuedAt:  time.Unix(j.IssuedAt, 0).UTC(),
		ExpiresAt: time.Unix(j.ExpiresAt, 0).UTC(),
		ID:        j.ID,
		AuthTime:  time.Unix(j.AuthTime, 0).UTC(),
		ACR:       j.ACR,
		AMR:       j.AMR,
		Scopes:    j.Scopes,
		Actor:     j.Actor,
	}
}
