//go:build integration

package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/files/v1/filesv1connect"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

// TestLiveDCFiles is driven by infra/files-dc-e2e/run.py. Its only fault API is
// an ordered stdin/stdout handshake: the test never selects or modifies a VM.
// All business operations use the real TLS browser → tunnel → Auth → Files path.
func TestLiveDCFiles(t *testing.T) {
	root := os.Getenv("FILES_DC_FIXTURE")
	if root == "" {
		t.Skip("four-VM DC fixture not configured")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
	defer cancel()
	roots := x509.NewCertPool()
	for _, name := range []string{"account/browser/ca.pem", "files/pki/ca.crt"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !roots.AppendCertsFromPEM(raw) {
			t.Fatal("fixture CA unavailable")
		}
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid fixture target")
			}
			raw, err := os.ReadFile(filepath.Join(root, "routing.json"))
			var targets map[string]string
			if err != nil || json.Unmarshal(raw, &targets) != nil || net.ParseIP(targets[host]) == nil {
				return nil, fmt.Errorf("unknown fixture target")
			}
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(targets[host], port))
		}}
	t.Cleanup(transport.CloseIdleConnections)
	const base = "https://frontdoor:8443"
	newClient := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Transport: transport, Jar: jar, Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	input := bufio.NewReader(io.LimitReader(os.Stdin, 64*1024))
	event := func(name string) {
		t.Helper()
		fmt.Fprintln(os.Stdout, "MM43_DC_EVENT "+name)
		line, err := input.ReadString('\n')
		var reply struct {
			Phase string `json:"phase"`
			OK    bool   `json:"ok"`
		}
		if err != nil || json.Unmarshal([]byte(line), &reply) != nil || reply.Phase != name || !reply.OK {
			t.Fatal("DC harness did not confirm " + name)
		}
		transport.CloseIdleConnections()
	}
	register := func() *http.Client {
		t.Helper()
		client := newClient()
		auth := authv1connect.NewAuthServiceClient(client, base)
		name := "dc-" + opaque(t).String()
		password := []byte("MM43-" + opaque(t).String())
		if _, err := auth.RegisterCredentials(ctx, browserRequest(&authv1.RegisterCredentialsRequest{Identifier: name, Password: password})); err != nil {
			t.Fatal("DC registration", connect.CodeOf(err))
		}
		if _, err := auth.Login(ctx, browserRequest(&authv1.LoginRequest{Identifier: name, Password: password})); err != nil {
			t.Fatal("DC login", connect.CodeOf(err))
		}
		return client
	}
	client := register()
	foreign := register()
	revoked := register()
	replay := newClient()
	origin, _ := url.Parse(base)
	replay.Jar.SetCookies(origin, revoked.Jar.Cookies(origin))
	if _, err := authv1connect.NewAuthServiceClient(revoked, base).Logout(ctx, browserRequest(&authv1.LogoutRequest{})); err != nil {
		t.Fatal("pre-fault logout", connect.CodeOf(err))
	}
	api := filesv1connect.NewFileServiceClient(client, base)
	anonymous := newClient()
	assertIsolation := func(id []byte) {
		t.Helper()
		for _, item := range []struct {
			client *http.Client
			code   connect.Code
		}{
			{foreign, connect.CodeNotFound}, {anonymous, connect.CodeUnauthenticated}, {replay, connect.CodeUnauthenticated},
		} {
			denied := filesv1connect.NewFileServiceClient(item.client, base)
			if _, err := denied.GetStatus(ctx, browserRequest(&filesv1.GetStatusRequest{FileId: id})); connect.CodeOf(err) != item.code {
				t.Fatal("owner/session isolation", connect.CodeOf(err), "want", item.code)
			}
			if _, err := denied.CreateDownload(ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: id})); connect.CodeOf(err) != item.code {
				t.Fatal("download isolation", connect.CodeOf(err), "want", item.code)
			}
		}
	}
	// The manifest spans two independently acknowledged immutable PUTs.
	pixels := image.NewNRGBA(image.Rect(0, 0, 2048, 1024))
	if _, err := rand.Read(pixels.Pix); err != nil {
		t.Fatal("random fixture")
	}
	for i := 3; i < len(pixels.Pix); i += 4 {
		pixels.Pix[i] = 255
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal("PNG fixture")
	}
	large := encoded.Bytes()
	if len(large) <= file.PartSize || len(large) > 2*file.PartSize {
		t.Fatal("expected two-part fixture")
	}
	makeRequest := func(data []byte) *filesv1.CreateUploadRequest {
		key := opaque(t)
		sum := sha256.Sum256(data)
		result := &filesv1.CreateUploadRequest{IdempotencyKey: key[:], MediaType: string(file.PNG), SizeBytes: uint64(len(data)), Sha256: sum[:]}
		for offset := 0; offset < len(data); offset += file.PartSize {
			part := data[offset:min(offset+file.PartSize, len(data))]
			hash := sha256.Sum256(part)
			result.Parts = append(result.Parts, &filesv1.PartManifest{SizeBytes: uint64(len(part)), Sha256: hash[:]})
		}
		return result
	}
	uploadParts := func(upload *filesv1.CreateUploadResponse, data []byte, only uint32) {
		t.Helper()
		for _, cap := range upload.Parts {
			if only != 0 && cap.PartNumber != only {
				continue
			}
			offset := int(cap.PartNumber-1) * file.PartSize
			part := data[offset:min(offset+file.PartSize, len(data))]
			request, err := http.NewRequestWithContext(ctx, "PUT", cap.Url, bytes.NewReader(part))
			if err != nil {
				t.Fatal("PUT construction")
			}
			for _, header := range cap.Headers {
				request.Header.Set(header.Name, header.Value)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("direct PUT transport")
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal("direct PUT rejected", response.StatusCode)
			}
		}
	}
	ready := func(id []byte) *filesv1.GetStatusResponse {
		t.Helper()
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			status, err := api.GetStatus(ctx, browserRequest(&filesv1.GetStatusRequest{FileId: id}))
			if err != nil {
				t.Fatal("READY polling", connect.CodeOf(err))
			}
			if status.Msg.State == filesv1.FileState_FILE_STATE_READY {
				return status.Msg
			}
			if status.Msg.State == filesv1.FileState_FILE_STATE_REJECTED {
				t.Fatal("fixture rejected")
			}
			time.Sleep(time.Second)
		}
		t.Fatal("READY deadline exceeded")
		return nil
	}
	download := func(id []byte, want *filesv1.GetStatusResponse, expected string) {
		t.Helper()
		result, err := api.CreateDownload(ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: id}))
		if err != nil {
			t.Fatal("surviving-DC download", connect.CodeOf(err))
		}
		location, err := url.Parse(result.Msg.Url)
		if err != nil || (expected != "" && location.Hostname() != expected) {
			t.Fatal("download routed to failed DC")
		}
		response, err := client.Get(result.Msg.Url)
		if err != nil {
			t.Fatal("delivery transport after DC transition")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, file.MaxSize+1))
		response.Body.Close()
		sum := sha256.Sum256(data)
		if readErr != nil || response.StatusCode != 200 || !bytes.Equal(sum[:], want.CleanSha256) {
			t.Fatal("acknowledged clean bytes changed")
		}
	}
	small := sample(t, file.PNG)
	created, err := api.CreateUpload(ctx, browserRequest(makeRequest(small)))
	if err != nil {
		t.Fatal("initial create", connect.CodeOf(err))
	}
	uploadParts(created.Msg, small, 0)
	if _, err := api.CompleteUpload(ctx, browserRequest(&filesv1.CompleteUploadRequest{FileId: created.Msg.FileId})); err != nil {
		t.Fatal("initial complete", connect.CodeOf(err))
	}
	confirmed := ready(created.Msg.FileId)
	download(created.Msg.FileId, confirmed, "")
	for _, dc := range []string{"dc-a", "dc-b"} {
		t.Log("testing full loss of " + dc)
		manifest := makeRequest(large)
		pending, err := api.CreateUpload(ctx, browserRequest(manifest))
		if err != nil {
			t.Fatal("multipart create", connect.CodeOf(err))
		}
		uploadParts(pending.Msg, large, 1)
		resumed, err := api.CreateUpload(ctx, browserRequest(manifest))
		if err != nil || !bytes.Equal(resumed.Msg.FileId, pending.Msg.FileId) || !slices.Equal(resumed.Msg.ReceivedParts, []uint32{1}) || len(resumed.Msg.Parts) != 1 {
			t.Fatal("part 1 not durably acknowledged before DC loss")
		}
		event("fault-" + dc)
		// Status and already-READY downloads must survive using the promoted
		// metadata replica and the remaining delivery/KMS pair.
		current, err := api.GetStatus(ctx, browserRequest(&filesv1.GetStatusRequest{FileId: created.Msg.FileId}))
		if err != nil || current.Msg.State != filesv1.FileState_FILE_STATE_READY || !bytes.Equal(current.Msg.CleanSha256, confirmed.CleanSha256) {
			t.Fatal("acknowledged metadata lost")
		}
		survivor := "delivery-b"
		if dc == "dc-b" {
			survivor = "delivery-a"
		}
		download(created.Msg.FileId, confirmed, survivor)
		assertIsolation(created.Msg.FileId)
		if _, err := api.CreateDownload(ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: pending.Msg.FileId})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatal("unfinished upload exposed during outage")
		}
		blocked := makeRequest(small)
		writeResult := make(chan error, 1)
		go func() {
			_, writeErr := api.CreateUpload(ctx, browserRequest(blocked))
			writeResult <- writeErr
		}()
		event("blocked-" + dc)
		select {
		case writeErr := <-writeResult:
			code := connect.CodeOf(writeErr)
			if writeErr == nil || (code != connect.CodeUnavailable && code != connect.CodeDeadlineExceeded) {
				t.Fatal("expected unavailable write without synchronous peer", code)
			}
		case <-ctx.Done():
			t.Fatal("blocked-write probe did not finish")
		}
		event("restore-" + dc)
		// Same idempotency key and original cookie; no imported process state.
		resumed, err = api.CreateUpload(ctx, browserRequest(manifest))
		if err != nil || !bytes.Equal(resumed.Msg.FileId, pending.Msg.FileId) || !slices.Equal(resumed.Msg.ReceivedParts, []uint32{1}) || len(resumed.Msg.Parts) != 1 {
			t.Fatal("acknowledged multipart state lost on recovery")
		}
		uploadParts(resumed.Msg, large, 0)
		if _, err := api.CompleteUpload(ctx, browserRequest(&filesv1.CompleteUploadRequest{FileId: pending.Msg.FileId})); err != nil {
			t.Fatal("complete after recovery", connect.CodeOf(err))
		}
		complete := ready(pending.Msg.FileId)
		download(pending.Msg.FileId, complete, "")
		retry, err := api.CreateUpload(ctx, browserRequest(blocked))
		if err != nil {
			t.Fatal("uncertain write could not resume after sync recovery", connect.CodeOf(err))
		}
		again, err := api.CreateUpload(ctx, browserRequest(blocked))
		if err != nil || !bytes.Equal(retry.Msg.FileId, again.Msg.FileId) {
			t.Fatal("uncertain acknowledgement duplicated upload")
		}
		assertIsolation(created.Msg.FileId)
		download(created.Msg.FileId, confirmed, "")
		t.Log("fencing, promotion, durable metadata/bytes, fail-closed writes, isolation and multipart resume passed")
	}
	if _, err := api.Delete(ctx, browserRequest(&filesv1.DeleteRequest{FileId: created.Msg.FileId})); err != nil {
		t.Fatal("post-recovery delete", connect.CodeOf(err))
	}
	if _, err := api.CreateDownload(ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: created.Msg.FileId})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("deleted file exposed")
	}
	t.Log("MM43_DC_E2E_PASS")
}
