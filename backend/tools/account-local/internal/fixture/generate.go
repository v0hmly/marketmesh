package fixture

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const lifetime = 7 * 24 * time.Hour
const topologyVersion = 3

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func write(root, name string, data []byte) error {
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}
func writeEnv(root, name string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if strings.ContainsAny(values[k], "\n\r'") {
			return errors.New("unsafe environment value")
		}
		fmt.Fprintf(&b, "%s='%s'\n", k, values[k])
	}
	return write(root, name, []byte(b.String()))
}

// Generate never rotates an existing fixture silently: persisted databases and session
// keys must remain paired. Expiration or origin changes require an explicit reset.
func Generate(root, port string) error {
	if !filepath.IsAbs(root) {
		return errors.New("FIXTURE_ROOT must be absolute")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1024 || p > 65535 {
		return errors.New("port must be between 1024 and 65535")
	}
	if marker, err := os.ReadFile(filepath.Join(root, "ready.json")); err == nil {
		var state struct {
			Port     string
			Expires  time.Time
			Topology int
		}
		if json.Unmarshal(marker, &state) != nil || state.Topology != topologyVersion || state.Port != port || time.Until(state.Expires) < time.Hour {
			return errors.New("fixture topology, expiration or origin changed; preserve old state and use a new project, or explicitly reset disposable data")
		}
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".owner" {
			return errors.New("incomplete fixture; explicit reset required")
		}
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err = os.Chmod(root, 0700); err != nil {
		return err
	}
	now := time.Now().UTC()
	expires := now.Add(lifetime)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MarketMesh local account fixture"}, NotBefore: now.Add(-time.Minute), NotAfter: expires, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// CA private key exists only in memory; each process sees just its own leaf.
	for i, name := range []string{"auth", "user", "gateway-in", "gateway-out", "frontdoor", "nats", "provision"} {
		leafKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return e
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Minute), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, DNSNames: []string{name}}
		if name == "frontdoor" {
			leaf.DNSNames = append(leaf.DNSNames, "localhost")
			leaf.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		if name == "auth" || name == "user" || name == "gateway-in" || name == "gateway-out" {
			u, _ := url.Parse("spiffe://marketmesh.test/test/" + name)
			leaf.URIs = []*url.URL{u}
		}
		switch name {
		case "auth":
			leaf.EmailAddresses = []string{"auth-publisher@marketmesh.test"}
		case "user":
			leaf.EmailAddresses = []string{"user-consumer@marketmesh.test"}
		case "provision":
			leaf.EmailAddresses = []string{"fixture-admin@marketmesh.test"}
		}
		cert, e := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if e != nil {
			return e
		}
		priv, e := x509.MarshalPKCS8PrivateKey(leafKey)
		if e != nil {
			return e
		}
		for file, data := range map[string][]byte{"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), "ca.pem": caPEM} {
			if e = write(root, name+"/"+file, data); e != nil {
				return e
			}
		}
	}
	if err = write(root, "browser/ca.pem", caPEM); err != nil {
		return err
	}
	// Public CA is readable by the unprivileged disposable browser. Parent state stays 0700.
	if err = os.Chmod(filepath.Join(root, "browser"), 0755); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(root, "browser/ca.pem"), 0644); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, "artifacts"), 0777); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(root, "artifacts"), 0777); err != nil {
		return err
	}
	signingPub, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	keys, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kid": "local", "private_key": base64.RawURLEncoding.EncodeToString(signingKey), "public_key": base64.RawURLEncoding.EncodeToString(signingPub), "sign_from": now.Add(-time.Minute), "sign_until": expires.Add(-time.Minute), "verify_until": expires}}})
	if err = write(root, "auth/keys.json", keys); err != nil {
		return err
	}
	mailKey := make([]byte, 32)
	if _, err := rand.Read(mailKey); err != nil {
		return err
	}
	if err := write(root, "auth/mail.key", mailKey); err != nil {
		return err
	}
	clear(mailKey)
	provision := map[string]string{}
	apps := map[string]map[string]string{}
	for _, name := range []string{"auth", "user", "gateway-in", "gateway-out"} {
		apps[name] = map[string]string{"SERVICE_VERSION": "local", "ENVIRONMENT": "test", "SERVICE_INSTANCE_ID": "account-local-" + name, "HTTP_ADDRESS": ":8080"}
	}
	admin, repl := secret(), secret()
	databaseEnv := map[string]string{"POSTGRES_DB": "postgres", "POSTGRES_USER": "fixture_admin", "POSTGRES_PASSWORD": admin, "REPLICATOR_PASSWORD": repl}
	for _, name := range []string{"auth", "user"} {
		rw, ro := secret(), secret()
		databaseEnv[strings.ToUpper(name)+"_RW_PASSWORD"] = rw
		databaseEnv[strings.ToUpper(name)+"_RO_PASSWORD"] = ro
		provision[strings.ToUpper(name)+"_ADMIN_DSN"] = "postgres://fixture_admin:" + admin + "@postgres-primary:5432/" + name + "?sslmode=disable"
		apps[name]["POSTGRES_RW_DSN"] = "postgres://" + name + "_rw:" + rw + "@postgres-primary:5432/" + name + "?sslmode=disable"
		apps[name]["POSTGRES_RO_DSN"] = "postgres://" + name + "_ro:" + ro + "@postgres-replica:5432/" + name + "?sslmode=disable"
		provision[strings.ToUpper(name)+"_RW_DSN"] = apps[name]["POSTGRES_RW_DSN"]
		provision[strings.ToUpper(name)+"_RO_DSN"] = apps[name]["POSTGRES_RO_DSN"]
	}
	if err = writeEnv(root, "postgres-primary/env", databaseEnv); err != nil {
		return err
	}
	if err = writeEnv(root, "postgres-replica/env", map[string]string{"PGDATA": "/var/lib/postgresql/18/replica", "PRIMARY_HOST": "postgres-primary", "REPLICATOR_PASSWORD": repl}); err != nil {
		return err
	}
	redisPassword := secret()
	if err = write(root, "redis/redis.conf", []byte("bind 0.0.0.0\nprotected-mode yes\nrequirepass "+redisPassword+"\nsave \"\"\nappendonly no\n")); err != nil {
		return err
	}
	merge := func(name string, values map[string]string) {
		for k, v := range values {
			apps[name][k] = v
		}
	}
	merge("auth", map[string]string{
		"AUTH_EMAIL_ENABLED": "true", "AUTH_EMAIL_KEY_FILE": "/secrets/mail.key",
		"AUTH_EMAIL_PUBLIC_ORIGIN":   "https://localhost:" + port,
		"AUTH_SMTP_ADDRESS":          "mailpit:1025",
		"AUTH_SMTP_PLAINTEXT_REASON": "isolated local Mailpit; external relay is not configured",
		"AUTH_SESSIONS_ENABLED":      "true", "AUTH_REGISTRATION_EVENTS_ENABLED": "true", "AUTH_REGISTRATION_PUBLISH_ENABLED": "true", "AUTH_SESSION_ISSUER": "auth.marketmesh", "AUTH_SESSION_KEYS_FILE": "/secrets/keys.json", "AUTH_TRUST_DOMAIN": "marketmesh.test", "AUTH_INTERNAL_ADDRESS": ":9091", "AUTH_INTERNAL_TLS_CERT_FILE": "/secrets/cert.pem", "AUTH_INTERNAL_TLS_KEY_FILE": "/secrets/key.pem", "AUTH_INTERNAL_CLIENT_CA_FILE": "/secrets/ca.pem", "AUTH_ALLOWED_ORIGINS": "https://localhost:" + port + ",https://127.0.0.1:" + port + ",https://frontdoor:8443", "AUTH_SESSION_AUDIENCES": `{"user":["user:profile:read","user:profile:write","user:addresses:read","user:addresses:write","user:settings:read","user:settings:write"]}`, "AUTH_REDIS_ADDRESS": "redis:6379", "AUTH_REDIS_PASSWORD": redisPassword, "AUTH_REDIS_PLAINTEXT_REASON": "isolated local account network; loopback-only disposable fixture", "AUTH_NATS_URL": "tls://nats:4222", "AUTH_NATS_SERVER_NAME": "nats", "AUTH_EVENTS_TRUST_DOMAIN": "marketmesh.test", "AUTH_NATS_TLS_CERT_FILE": "/secrets/cert.pem", "AUTH_NATS_TLS_KEY_FILE": "/secrets/key.pem", "AUTH_NATS_TLS_CA_FILE": "/secrets/ca.pem",
	})
	merge("user", map[string]string{"USER_PROFILE_ENABLED": "true", "USER_ADDRESSES_ENABLED": "true", "USER_TRUST_DOMAIN": "marketmesh.test", "USER_TLS_CERT_FILE": "/secrets/cert.pem", "USER_TLS_KEY_FILE": "/secrets/key.pem", "USER_TLS_CLIENT_CA_FILE": "/secrets/ca.pem", "USER_GRPC_ADDRESS": ":9092", "USER_AUTH_TARGET": "auth:9091", "USER_AUTH_SERVER_NAME": "auth", "USER_AUTH_CA_FILE": "/secrets/ca.pem", "USER_AUTH_ISSUER": "auth.marketmesh", "USER_NATS_URL": "tls://nats:4222", "USER_NATS_SERVER_NAME": "nats", "USER_NATS_TLS_CERT_FILE": "/secrets/cert.pem", "USER_NATS_TLS_KEY_FILE": "/secrets/key.pem", "USER_NATS_TLS_CA_FILE": "/secrets/ca.pem"})
	merge("gateway-in", map[string]string{"DATA_CENTER": "dc-a", "GRPC_ADDRESS": ":8443", "TLS_CERT_FILE": "/secrets/cert.pem", "TLS_KEY_FILE": "/secrets/key.pem", "TLS_CLIENT_CA_FILE": "/secrets/ca.pem", "EXPECTED_GATEWAY_OUT_URI": "spiffe://marketmesh.test/test/gateway-out", "AUTH_BROWSER_ENABLED": "true", "USER_BROWSER_ENABLED": "true", "USER_ADDRESSES_BROWSER_ENABLED": "true", "PUBLIC_TLS_CERT_FILE": "/secrets/cert.pem", "PUBLIC_TLS_KEY_FILE": "/secrets/key.pem"})
	merge("gateway-out", map[string]string{"GATEWAY_IN_TARGET": "gateway-in:8443", "GATEWAY_IN_SERVER_NAME": "gateway-in", "EXPECTED_GATEWAY_IN_URI": "spiffe://marketmesh.test/test/gateway-in", "INTERNAL_TARGET": "user:9092", "INTERNAL_SERVER_NAME": "user", "EXPECTED_INTERNAL_URI": "spiffe://marketmesh.test/test/user", "AUTH_BROWSER_ENABLED": "true", "USER_BROWSER_ENABLED": "true", "USER_ADDRESSES_BROWSER_ENABLED": "true", "AUTH_TARGET": "auth:9091", "AUTH_SERVER_NAME": "auth", "EXPECTED_AUTH_URI": "spiffe://marketmesh.test/test/auth"})
	apps["user"]["USER_SETTINGS_ENABLED"] = "true"
	apps["gateway-in"]["USER_SETTINGS_BROWSER_ENABLED"] = "true"
	apps["gateway-out"]["USER_SETTINGS_BROWSER_ENABLED"] = "true"
	// This local topology has a fixed ingress pair, without readiness-aware balancing.
	apps["gateway-out"]["TUNNEL_PERIODIC_REDISCOVERY_ENABLED"] = "false"
	for _, prefix := range []string{"TUNNEL", "INTERNAL", "AUTH"} {
		merge("gateway-out", map[string]string{prefix + "_TLS_CERT_FILE": "/secrets/cert.pem", prefix + "_TLS_KEY_FILE": "/secrets/key.pem", prefix + "_TLS_ROOT_CA_FILE": "/secrets/ca.pem"})
	}
	for name, env := range apps {
		if err = writeEnv(root, name+"/env", env); err != nil {
			return err
		}
	}
	if err = writeEnv(root, "provision/env", provision); err != nil {
		return err
	}
	if err = write(root, "nats/nats.conf", []byte(natsConfig)); err != nil {
		return err
	}
	marker, _ := json.Marshal(struct {
		Port     string
		Expires  time.Time
		Topology int
	}{port, expires, topologyVersion})
	return write(root, "ready.json", marker)
}

const natsConfig = `port: 4222
server_name: account-local
jetstream { store_dir: "/data/jetstream", max_mem_store: 64MB, max_file_store: 128MB }
max_payload: 16384
tls { cert_file: "/secrets/cert.pem", key_file: "/secrets/key.pem", ca_file: "/secrets/ca.pem", min_version: "1.3", verify_and_map: true, timeout: 2 }
authorization { users: [
 { user: "auth-publisher@marketmesh.test", permissions: { publish: { allow: ["auth.account.registered.v1"] }, subscribe: { allow: ["_INBOX.>"] } } },
 { user: "user-consumer@marketmesh.test", permissions: { publish: { allow: ["$JS.API.CONSUMER.INFO.AUTH_REGISTRATION.USER_PROFILES_V1", "$JS.API.CONSUMER.MSG.NEXT.AUTH_REGISTRATION.USER_PROFILES_V1", "$JS.ACK.AUTH_REGISTRATION.USER_PROFILES_V1.>"] }, subscribe: { allow: ["_INBOX.>"] } } },
 { user: "fixture-admin@marketmesh.test", permissions: { publish: { allow: [">"] }, subscribe: { allow: [">"] } } }
] }
`
