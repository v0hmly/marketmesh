package s3store

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

func bucket(t *testing.T, server *httptest.Server) *Bucket {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	b, err := NewBucket(Config{APIEndpoint: server.URL, PublicEndpoint: server.URL, Bucket: "clean", KMSKey: "clean-key", Control: Credential{AccessKey: "control-test", SecretKey: "control-test-secret"}, Capability: Credential{AccessKey: "download-test", SecretKey: "download-test-secret"}, TLS: &tls.Config{RootCAs: roots}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDownloadFailoverUsesRemainingTTL(t *testing.T) {
	sum := file.Digest(sha256.Sum256([]byte("pdf")))
	r := file.Record{ID: file.ID{1}, Owner: file.Owner{Tenant: file.ID{2}, Subject: file.ID{3}}, ObjectKey: file.ID{4}.String(), State: file.Ready, Manifest: file.Manifest{Format: file.PDF, Size: 3, SHA256: sum, Parts: []file.Part{{Size: 3, SHA256: sum}}}, CleanFormat: file.PDF, CleanSize: 3, CleanSHA256: sum}
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	fast := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3")
		w.Header().Set("X-Amz-Server-Side-Encryption", "aws:kms")
		w.Header().Set("X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", "clean-key")
		w.Header().Set("X-Amz-Meta-Clean-Sha256", hex.EncodeToString(sum[:]))
		w.WriteHeader(200)
	}))
	defer fast.Close()
	store := &Store{Clean: []*Bucket{bucket(t, slow), bucket(t, fast)}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	until := time.Now().Add(10 * time.Second)
	capability, err := store.SignDownload(ctx, r, until)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(capability.URL)
	if err != nil {
		t.Fatal("malformed capability")
	}
	if parsed.Host != fast.Listener.Addr().String() {
		t.Fatal("wrong DC")
	}
	issued, err := time.Parse("20060102T150405Z", parsed.Query().Get("X-Amz-Date"))
	if err != nil {
		t.Fatal("signature date")
	}
	expires, err := strconv.Atoi(parsed.Query().Get("X-Amz-Expires"))
	if err != nil {
		t.Fatal("signature expiry")
	}
	if issued.Add(time.Duration(expires)*time.Second).After(until) || !capability.ExpiresAt.Equal(until) {
		t.Fatal("URL exceeds reported expiry")
	}
}

func TestEndpointCannotRedirectCredentials(t *testing.T) {
	for _, raw := range []string{"http://store.local", "https://user:secret@store.local", "https://store.local/path", "https://store.local?token=value", "https://store.local#fragment"} {
		if _, err := endpoint(raw); err == nil {
			t.Fatal("invalid origin accepted")
		}
	}
}
