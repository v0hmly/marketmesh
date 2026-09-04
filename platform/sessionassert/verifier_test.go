package sessionassert

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Инвариант ADR-0005: утверждение для одного сервиса отклоняется другим.
func TestVerifyWrongAudience(t *testing.T) {
	iss, pub := newTestIssuer(t, "k1")
	token, err := iss.Issue(validParams())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	other, err := NewVerifier("auth.marketmesh", "billing-service", ks)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := other.Verify(token); !errors.Is(err, ErrBadAudience) {
		t.Fatalf("Verify error = %v, want ErrBadAudience", err)
	}
}

func TestVerifyWrongIssuer(t *testing.T) {
	pub, priv := testKeys(t)
	token := craftToken(t, priv, validHeader("k1"), validPayload())

	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	v, err := NewVerifier("other-issuer", "user-service", ks)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := v.Verify(token); !errors.Is(err, ErrBadIssuer) {
		t.Fatalf("Verify error = %v, want ErrBadIssuer", err)
	}
}

// Инвариант ADR-0005: просроченное утверждение отклоняется.
func TestVerifyExpired(t *testing.T) {
	pub, priv := testKeys(t)
	v := newTestVerifier(t, "k1", pub)

	payload := validPayload()
	payload["iat"] = time.Now().Add(-time.Hour).Unix()
	payload["exp"] = time.Now().Add(-time.Minute).Unix() // за пределами leeway
	token := craftToken(t, priv, validHeader("k1"), payload)
	if _, err := v.Verify(token); !errors.Is(err, ErrExpired) {
		t.Fatalf("Verify error = %v, want ErrExpired", err)
	}

	// Истёк в пределах leeway — ещё принимается.
	payload["exp"] = time.Now().Add(-10 * time.Second).Unix()
	token = craftToken(t, priv, validHeader("k1"), payload)
	if _, err := v.Verify(token); err != nil {
		t.Fatalf("Verify within leeway: %v", err)
	}
}

func TestVerifyNotYetValid(t *testing.T) {
	pub, priv := testKeys(t)
	v := newTestVerifier(t, "k1", pub)

	payload := validPayload()
	payload["iat"] = time.Now().Add(time.Hour).Unix()
	payload["exp"] = time.Now().Add(2 * time.Hour).Unix()
	token := craftToken(t, priv, validHeader("k1"), payload)
	if _, err := v.Verify(token); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("Verify error = %v, want ErrNotYetValid", err)
	}

	// iat чуть в будущем в пределах leeway — принимается.
	payload["iat"] = time.Now().Add(10 * time.Second).Unix()
	token = craftToken(t, priv, validHeader("k1"), payload)
	if _, err := v.Verify(token); err != nil {
		t.Fatalf("Verify within leeway: %v", err)
	}
}

// Инвариант ADR-0005: утверждение неверного типа отклоняется.
func TestVerifyBadType(t *testing.T) {
	pub, priv := testKeys(t)
	v := newTestVerifier(t, "k1", pub)

	header := validHeader("k1")
	header["typ"] = "jwt"
	token := craftToken(t, priv, header, validPayload())
	if _, err := v.Verify(token); !errors.Is(err, ErrBadType) {
		t.Fatalf("header typ: Verify error = %v, want ErrBadType", err)
	}

	payload := validPayload()
	payload["typ"] = "something-else"
	token = craftToken(t, priv, validHeader("k1"), payload)
	if _, err := v.Verify(token); !errors.Is(err, ErrBadType) {
		t.Fatalf("claims typ: Verify error = %v, want ErrBadType", err)
	}
}

func TestVerifyBadSignature(t *testing.T) {
	pub, _ := testKeys(t)
	_, otherPriv := testKeys(t)
	v := newTestVerifier(t, "k1", pub)

	// Подпись другим ключом.
	token := craftToken(t, otherPriv, validHeader("k1"), validPayload())
	if _, err := v.Verify(token); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: Verify error = %v, want ErrBadSignature", err)
	}

	// Подделанная подпись: токен подписан верно, но payload заменён.
	_, priv := testKeys(t)
	ks := NewStaticKeySet()
	pub2 := priv.Public().(ed25519.PublicKey)
	if err := ks.Add("k1", pub2); err != nil {
		t.Fatalf("Add: %v", err)
	}
	v2, err := NewVerifier("auth.marketmesh", "user-service", ks)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	good := craftToken(t, priv, validHeader("k1"), validPayload())
	parts := strings.Split(good, ".")
	tamperedPayload := validPayload()
	tamperedPayload["sub"] = "u-attacker"
	raw, err := json.Marshal(tamperedPayload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(raw) + "." + parts[2]
	if _, err := v2.Verify(forged); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered payload: Verify error = %v, want ErrBadSignature", err)
	}
}

func TestVerifyUnknownKeyID(t *testing.T) {
	_, priv := testKeys(t)
	token := craftToken(t, priv, validHeader("k-unknown"), validPayload())

	pub, _ := testKeys(t)
	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	v, err := NewVerifier("auth.marketmesh", "user-service", ks)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := v.Verify(token); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("Verify error = %v, want ErrUnknownKeyID", err)
	}
}

// RFC 8725: значение alg из заголовка игнорируется, подпись всегда
// проверяется как EdDSA/Ed25519.
func TestVerifyAlgIgnored(t *testing.T) {
	pub, priv := testKeys(t)
	v := newTestVerifier(t, "k1", pub)

	for _, alg := range []string{"none", "HS256", "RS256"} {
		header := validHeader("k1")
		header["alg"] = alg

		// Валидная подпись Ed25519 — токен принимается, alg не влияет.
		token := craftToken(t, priv, header, validPayload())
		if _, err := v.Verify(token); err != nil {
			t.Fatalf("alg=%q with valid signature: %v", alg, err)
		}

		// Невалидная подпись — отклоняется даже при alg:none.
		_, otherPriv := testKeys(t)
		token = craftToken(t, otherPriv, header, validPayload())
		if _, err := v.Verify(token); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("alg=%q with bad signature: Verify error = %v, want ErrBadSignature", alg, err)
		}
	}
}

func TestVerifyRequiredScopes(t *testing.T) {
	iss, pub := newTestIssuer(t, "k1")
	p := validParams()
	p.Scopes = []string{"profile:read", "orders:write"}
	token, err := iss.Issue(p)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	v := newTestVerifier(t, "k1", pub, RequireScopes("profile:read", "orders:write"))
	if _, err := v.Verify(token); err != nil {
		t.Fatalf("Verify with all scopes: %v", err)
	}

	missing := newTestVerifier(t, "k1", pub, RequireScopes("billing:read"))
	if _, err := missing.Verify(token); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("Verify error = %v, want ErrMissingScope", err)
	}
}

// Ротация ключей с перекрытием: пока оба kid в наборе, валидны токены обоих;
// после удаления старого kid его токены отклоняются.
func TestKeyRotation(t *testing.T) {
	oldIss, oldPub := newTestIssuer(t, "k-old")
	newIss, newPub := newTestIssuer(t, "k-new")

	ks := NewStaticKeySet()
	if err := ks.Add("k-old", oldPub); err != nil {
		t.Fatalf("Add old: %v", err)
	}
	if err := ks.Add("k-new", newPub); err != nil {
		t.Fatalf("Add new: %v", err)
	}
	v, err := NewVerifier("auth.marketmesh", "user-service", ks)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	oldToken, err := oldIss.Issue(validParams())
	if err != nil {
		t.Fatalf("Issue old: %v", err)
	}
	newToken, err := newIss.Issue(validParams())
	if err != nil {
		t.Fatalf("Issue new: %v", err)
	}

	// Период перекрытия: оба ключа принимаются.
	if _, err := v.Verify(oldToken); err != nil {
		t.Fatalf("Verify old during overlap: %v", err)
	}
	if _, err := v.Verify(newToken); err != nil {
		t.Fatalf("Verify new during overlap: %v", err)
	}

	// Перекрытие завершено: старый kid удалён.
	ks.Remove("k-old")
	if _, err := v.Verify(oldToken); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("Verify old after removal = %v, want ErrUnknownKeyID", err)
	}
	if _, err := v.Verify(newToken); err != nil {
		t.Fatalf("Verify new after removal: %v", err)
	}
}

func TestStaticKeySetValidation(t *testing.T) {
	pub, _ := testKeys(t)
	ks := NewStaticKeySet()
	if err := ks.Add("", pub); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("empty kid: %v", err)
	}
	if err := ks.Add("k1", pub[:10]); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("short key: %v", err)
	}
	if _, err := ks.Key("absent"); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("unknown kid: %v", err)
	}
	// Удаление отсутствующего ключа — не ошибка.
	ks.Remove("absent")
}

// Потокобезопасность StaticKeySet под race detector.
func TestStaticKeySetConcurrent(t *testing.T) {
	pub, _ := testKeys(t)
	ks := NewStaticKeySet()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kid := "k" + strings.Repeat("x", i+1)
			for j := 0; j < 100; j++ {
				_ = ks.Add(kid, pub)
				_, _ = ks.Key(kid)
				ks.Remove(kid)
			}
		}(i)
	}
	wg.Wait()
}

func TestVerifyMalformed(t *testing.T) {
	pub, priv := testKeys(t)
	iss, err := NewIssuer(priv, "k1", "auth.marketmesh")
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	validToken, err := iss.Issue(validParams())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parts := strings.Split(validToken, ".")

	enc := base64.RawURLEncoding
	badJSON := enc.EncodeToString([]byte("{not json"))
	badHeaderTyp := enc.EncodeToString([]byte(`{"alg":"EdDSA","typ":123,"kid":"k1"}`))
	headerJSON, err := json.Marshal(validHeader("k1"))
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	// Payload с битым JSON, но валидной подписью, чтобы дойти до разбора.
	signedBadPayload := signCompact(priv, headerJSON, []byte("{not json"))

	cases := map[string]string{
		"empty":              "",
		"garbage":            "not-a-token",
		"two segments":       parts[0] + "." + parts[1],
		"four segments":      validToken + ".extra",
		"empty header":       "." + parts[1] + "." + parts[2],
		"empty payload":      parts[0] + ".." + parts[2],
		"empty signature":    parts[0] + "." + parts[1] + ".",
		"non-base64 header":  "###" + "." + parts[1] + "." + parts[2],
		"non-base64 payload": parts[0] + ".###." + parts[2],
		"non-base64 sig":     parts[0] + "." + parts[1] + ".###",
		"padded base64":      parts[0] + "=." + parts[1] + "." + parts[2],
		"bad header JSON":    badJSON + "." + parts[1] + "." + parts[2],
		"non-string typ":     badHeaderTyp + "." + parts[1] + "." + parts[2],
		"bad payload JSON":   signedBadPayload,
		"short signature":    parts[0] + "." + parts[1] + "." + enc.EncodeToString([]byte("short")),
		"no kid":             enc.EncodeToString([]byte(`{"alg":"EdDSA","typ":"`+TokenType+`"}`)) + "." + parts[1] + "." + parts[2],
	}

	v := newTestVerifier(t, "k1", pub)
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(token)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			// Текст ошибки не должен содержать сырой токен.
			if token != "" && strings.Contains(err.Error(), token) {
				t.Fatalf("error leaks raw token: %v", err)
			}
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("error = %v, want ErrMalformed", err)
			}
		})
	}
}

// Claims.String не выводит сырой токен и профильные данные отсутствуют
// по построению типа.
func TestClaimsStringNoToken(t *testing.T) {
	iss, pub := newTestIssuer(t, "k1")
	token, err := iss.Issue(validParams())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := newTestVerifier(t, "k1", pub).Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if strings.Contains(claims.String(), token) {
		t.Fatalf("String leaks token: %s", claims)
	}
}
