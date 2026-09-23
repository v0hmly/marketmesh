package workloadid

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestIdentityStringParseRoundTrip(t *testing.T) {
	identities := []Identity{
		{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-in"},
		{TrustDomain: "marketmesh.test", Environment: "dev", Role: "user", Instance: "user-7f9c"},
		{TrustDomain: "mm", Environment: "a1", Role: "r2-d2"},
	}
	for _, identity := range identities {
		parsed, err := ParseURI(identity.String())
		if err != nil {
			t.Fatalf("ParseURI(%q): %v", identity.String(), err)
		}
		if parsed != identity {
			t.Fatalf("round trip mismatch: got %+v, want %+v", parsed, identity)
		}
	}
}

func TestParseURIRejectsInvalid(t *testing.T) {
	uris := []string{
		"",
		"not a uri",
		"https://marketmesh.test/prod/gateway-in",          // чужая схема
		"SPIFFE://marketmesh.test/prod/gateway-in",         // схема в верхнем регистре
		"spiffe://marketmesh.test",                         // без пути
		"spiffe://marketmesh.test/",                        // пустые сегменты
		"spiffe://marketmesh.test/prod",                    // без роли
		"spiffe://marketmesh.test/prod/gateway-in/x/extra", // лишний сегмент
		"spiffe://marketmesh.test//gateway-in",             // пустая среда
		"spiffe://marketmesh.test/prod/",                   // пустая роль
		"spiffe://marketmesh.test/prod/gateway-in/",        // завершающий слэш
		"spiffe://marketmesh.test/prod/gateway-in?q=1",     // query
		"spiffe://marketmesh.test/prod/gateway-in#frag",    // fragment
		"spiffe://user@marketmesh.test/prod/gateway-in",    // userinfo
		"spiffe://marketmesh.test/PROD/gateway-in",         // верхний регистр среды
		"spiffe://marketmesh.test/prod/Gateway_In",         // недопустимые символы роли
		"spiffe://marketmesh.test/-prod/gateway-in",        // ведущий дефис
		"spiffe://marketmesh.test/prod-/gateway-in",        // завершающий дефис
		"spiffe://marketmesh.test/prod/gateway-in/%2e",     // percent-encoding
		"spiffe://MarketMesh.test/prod/gateway-in",         // верхний регистр домена
		"spiffe://marketmesh..test/prod/gateway-in",        // пустая метка домена
		"spiffe:///prod/gateway-in",                        // пустой домен
		"spiffe://marketmesh.test/prod/gateway_in",         // подчёркивание в роли
	}
	uris = append(uris, "spiffe://marketmesh.test/prod/"+strings.Repeat("a", 33))

	for _, raw := range uris {
		if _, err := ParseURI(raw); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("ParseURI(%q): got %v, want ErrInvalidIdentity", raw, err)
		}
	}
}

func TestIdentityValidate(t *testing.T) {
	valid := Identity{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-in"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid): %v", err)
	}
	invalid := []Identity{
		{},
		{TrustDomain: "marketmesh.test", Environment: "prod"},
		{TrustDomain: "marketmesh.test", Role: "gateway-in"},
		{Environment: "prod", Role: "gateway-in"},
		{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-in", Instance: "BAD"},
	}
	for _, identity := range invalid {
		if err := identity.Validate(); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("Validate(%+v): got %v, want ErrInvalidIdentity", identity, err)
		}
	}
}

func TestIdentityFromCertificate(t *testing.T) {
	ca := newTestCA(t)

	t.Run("valid", func(t *testing.T) {
		leaf := ca.issueLeaf(t, testLeafConfig{})
		identity, err := IdentityFromCertificate(leaf)
		if err != nil {
			t.Fatalf("IdentityFromCertificate: %v", err)
		}
		want := Identity{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-in"}
		if identity != want {
			t.Fatalf("got %+v, want %+v", identity, want)
		}
	})

	t.Run("without uri san", func(t *testing.T) {
		leaf := ca.issueLeaf(t, testLeafConfig{uris: []*url.URL{}})
		if _, err := IdentityFromCertificate(leaf); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("got %v, want ErrInvalidIdentity", err)
		}
	})

	t.Run("two uri sans", func(t *testing.T) {
		leaf := ca.issueLeaf(t, testLeafConfig{uris: []*url.URL{
			mustParseURL(t, "spiffe://marketmesh.test/prod/gateway-in"),
			mustParseURL(t, "spiffe://marketmesh.test/prod/other"),
		}})
		if _, err := IdentityFromCertificate(leaf); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("got %v, want ErrInvalidIdentity", err)
		}
	})

	t.Run("foreign uri san", func(t *testing.T) {
		leaf := ca.issueLeaf(t, testLeafConfig{
			uris: []*url.URL{mustParseURL(t, "https://marketmesh.test/prod/gateway-in")},
		})
		if _, err := IdentityFromCertificate(leaf); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("got %v, want ErrInvalidIdentity", err)
		}
	})

	t.Run("nil certificate", func(t *testing.T) {
		if _, err := IdentityFromCertificate(nil); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("got %v, want ErrInvalidIdentity", err)
		}
	})
}
