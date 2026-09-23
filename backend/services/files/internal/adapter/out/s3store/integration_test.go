//go:build integration

package s3store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

// This test uses disposable, TLS-enabled SeaweedFS + external OpenBao. The fixture
// path contains private test credentials and is never printed by assertions.
func TestLiveImmutableResumableParts(t *testing.T) {
	root := os.Getenv("FILES_FIXTURE")
	if root == "" {
		t.Skip("FILES_FIXTURE not configured")
	}
	raw, err := os.ReadFile(filepath.Join(root, "credentials.json"))
	if err != nil {
		t.Fatal("fixture credentials unavailable")
	}
	var credentials map[string]map[string]struct {
		AccessKey string `json:"accessKey"`
		SecretKey string `json:"secretKey"`
	}
	if json.Unmarshal(raw, &credentials) != nil {
		t.Fatal("invalid fixture credentials")
	}
	ca, err := os.ReadFile(filepath.Join(root, "pki/ca.crt"))
	if err != nil {
		t.Fatal("fixture CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid CA")
	}
	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	bucket, err := NewBucket(Config{APIEndpoint: "https://quarantine:8333", PublicEndpoint: "https://quarantine:8333", Bucket: "quarantine", KMSKey: "test-quarantine", Control: Credential{AccessKey: credentials["quarantine"]["worker"].AccessKey, SecretKey: credentials["quarantine"]["worker"].SecretKey}, Capability: Credential{AccessKey: credentials["quarantine"]["capability"].AccessKey, SecretKey: credentials["quarantine"]["capability"].SecretKey}, TLS: tlsConfig})
	if err != nil {
		t.Fatal("invalid bucket config")
	}
	store := &Store{Quarantine: bucket}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var id, object file.ID
	rand.Read(id[:])
	rand.Read(object[:])
	first := bytes.Repeat([]byte{0x36}, file.PartSize)
	last := []byte("last part")
	combined := sha256.New()
	combined.Write(first)
	combined.Write(last)
	var full file.Digest
	copy(full[:], combined.Sum(nil))
	r := file.Record{ID: id, Owner: file.Owner{Tenant: file.ID{1}, Subject: file.ID{2}}, ObjectKey: object.String(), State: file.Uploading, Manifest: file.Manifest{Format: file.PDF, Size: int64(len(first) + len(last)), SHA256: full, Parts: []file.Part{{Size: int64(len(first)), SHA256: file.Digest(sha256.Sum256(first))}, {Size: int64(len(last)), SHA256: file.Digest(sha256.Sum256(last))}}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r.UploadID, err = store.Begin(ctx, r)
	if err != nil {
		t.Fatal("begin failed")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = store.Abort(cleanup, r)
	})
	partOne, err := store.SignPart(ctx, r, 1, time.Now().Add(file.CapabilityTTL))
	if err != nil {
		t.Fatal("sign part failed")
	}
	for _, origin := range []string{"https://localhost:8443", "https://foreign.example"} {
		preflight, _ := http.NewRequestWithContext(ctx, "OPTIONS", partOne.URL, nil)
		preflight.Header.Set("Origin", origin)
		preflight.Header.Set("Access-Control-Request-Method", "PUT")
		preflight.Header.Set("Access-Control-Request-Headers", "if-none-match,x-amz-checksum-sha256,x-amz-server-side-encryption,x-amz-server-side-encryption-aws-kms-key-id,x-amz-meta-file-id,x-amz-meta-upload-id")
		response, err := client.Do(preflight)
		if err != nil {
			t.Fatal("CORS transport failed")
		}
		response.Body.Close()
		allowed := response.Header.Get("Access-Control-Allow-Origin")
		if origin == "https://localhost:8443" && allowed != origin {
			t.Fatal("upload preflight rejected", response.StatusCode)
		}
		if origin != "https://localhost:8443" && allowed != "" {
			t.Fatal("foreign browser origin accepted")
		}
	}
	request := func(cap files.Capability, body []byte, method string, rawURL string) (int, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		for key, value := range cap.Headers {
			req.Header.Set(key, value)
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		io.Copy(io.Discard, io.LimitReader(response.Body, 8192))
		return response.StatusCode, nil
	}
	wrong := append([]byte(nil), first...)
	wrong[0] ^= 1
	if code, err := request(partOne, wrong, http.MethodPut, partOne.URL); err != nil || code != 400 {
		t.Fatal("checksum mismatch accepted or unexpected transport failure", code)
	}
	if code, err := request(partOne, first[:len(first)-1], http.MethodPut, partOne.URL); err != nil || code != 403 {
		t.Fatal("size mutation accepted", code)
	}
	parsed, _ := url.Parse(partOne.URL)
	query := parsed.Query()
	query.Set("X-Amz-Expires", "3600")
	parsed.RawQuery = query.Encode()
	if code, err := request(partOne, first, http.MethodPut, parsed.String()); err != nil || code != 403 {
		t.Fatal("TTL mutation accepted", code)
	}
	if code, err := request(partOne, nil, http.MethodGet, partOne.URL); err != nil || code != 403 {
		t.Fatal("GET with upload capability accepted", code)
	}
	parsed, _ = url.Parse(partOne.URL)
	parsed.Path = strings.Replace(parsed.Path, "/0001", "/0002", 1)
	if code, err := request(partOne, first, http.MethodPut, parsed.String()); err != nil || code != 403 {
		t.Fatal("object key mutation accepted", code)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := request(partOne, first, http.MethodPut, partOne.URL)
			if err != nil {
				codes <- 0
			} else {
				codes <- code
			}
		}()
	}
	wg.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		if code == 200 {
			successes++
		} else if code != 412 {
			t.Fatal("unexpected conditional write result", code)
		}
	}
	if successes != 1 {
		t.Fatal("parallel replay overwrote object", successes)
	}
	parts, err := store.Parts(ctx, r)
	if err != nil || len(parts) != 1 || parts[0].Number != 1 || parts[0].SHA256 != r.Manifest.Parts[0].SHA256 {
		t.Fatal("accepted part not resumable")
	}
	if complete, err := store.Completed(ctx, r); err != nil || complete {
		t.Fatal("incomplete manifest accepted")
	}
	partTwo, err := store.SignPart(ctx, r, 2, time.Now().Add(file.CapabilityTTL))
	if err != nil {
		t.Fatal("sign remaining part")
	}
	if code, err := request(partTwo, last, http.MethodPut, partTwo.URL); err != nil || code != 200 {
		t.Fatal("resume remaining part failed", code)
	}
	if complete, err := store.Completed(ctx, r); err != nil || !complete {
		t.Fatal("complete immutable manifest not recognized")
	}
	if complete, err := store.Completed(ctx, r); err != nil || !complete {
		t.Fatal("repeated complete changed result")
	}
}
