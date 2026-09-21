//go:build integration

package app

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/files/v1/filesv1connect"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

func browserRequest[T any](value *T) *connect.Request[T] {
	request := connect.NewRequest(value)
	request.Header().Set("Origin", "https://frontdoor:8443")
	request.Header().Set("Sec-Fetch-Site", "same-origin")
	return request
}
func TestLiveControlThroughAuthAndTunnel(t *testing.T) {
	base := os.Getenv("FILES_CONTROL_BASE")
	if base == "" {
		t.Skip("private control fixture not configured")
	}
	f := live(t)
	ca, err := roots("/account-ca.pem")
	if err != nil {
		t.Fatal("account CA unavailable")
	}
	storageCA, err := os.ReadFile("/pki/ca.crt")
	if err != nil || !ca.AppendCertsFromPEM(storageCA) {
		t.Fatal("storage CA unavailable")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	login := func() (*http.Client, authv1connect.AuthServiceClient) {
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Transport: transport, Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		auth := authv1connect.NewAuthServiceClient(client, base)
		name := "files-" + opaque(t).String()
		password := []byte("Mm43-" + opaque(t).String())
		if _, err := auth.RegisterCredentials(f.ctx, browserRequest(&authv1.RegisterCredentialsRequest{Identifier: name, Password: password})); err != nil {
			t.Fatal("registration failed", connect.CodeOf(err))
		}
		if _, err := auth.Login(f.ctx, browserRequest(&authv1.LoginRequest{Identifier: name, Password: password})); err != nil {
			t.Fatal("login failed", connect.CodeOf(err))
		}
		return client, auth
	}
	client, auth := login()
	api := filesv1connect.NewFileServiceClient(client, base)
	source := sample(t, file.PNG)
	sum := sha256.Sum256(source)
	key := opaque(t)
	create := &filesv1.CreateUploadRequest{IdempotencyKey: key[:], MediaType: string(file.PNG), SizeBytes: uint64(len(source)), Sha256: sum[:], Parts: []*filesv1.PartManifest{{SizeBytes: uint64(len(source)), Sha256: sum[:]}}}
	started, err := api.CreateUpload(f.ctx, browserRequest(create))
	if err != nil {
		t.Fatal("create through tunnel", connect.CodeOf(err))
	}
	if len(started.Msg.Parts) != 1 {
		t.Fatal("unexpected capabilities")
	}
	id := bytes.Clone(started.Msg.FileId)
	t.Log("MM43_TUNNEL_RESTART_POINT")
	// The harness restarts gateway-out here. This client retains only its cookie
	// and idempotency key; no process-local upload state is carried to the new pod.
	time.Sleep(3 * time.Second)
	var resumed *connect.Response[filesv1.CreateUploadResponse]
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resumed, err = api.CreateUpload(f.ctx, browserRequest(create))
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil || resumed == nil || !bytes.Equal(resumed.Msg.FileId, id) {
		t.Fatal("upload lost across tunnel restart")
	}
	for _, cap := range resumed.Msg.Parts {
		request, err := http.NewRequestWithContext(f.ctx, "PUT", cap.Url, bytes.NewReader(source))
		if err != nil {
			t.Fatal("PUT construction")
		}
		for _, h := range cap.Headers {
			request.Header.Set(h.Name, h.Value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("direct upload failed")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("direct upload rejected", response.StatusCode)
		}
	}
	if _, err := api.CreateDownload(f.ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("pre-READY file exposed")
	}
	if _, err := api.CompleteUpload(f.ctx, browserRequest(&filesv1.CompleteUploadRequest{FileId: id})); err != nil {
		t.Fatal("complete through tunnel", connect.CodeOf(err))
	}
	var ready *filesv1.GetStatusResponse
	for range 200 {
		response, err := api.GetStatus(f.ctx, browserRequest(&filesv1.GetStatusRequest{FileId: id}))
		if err != nil {
			t.Fatal("status through tunnel", connect.CodeOf(err))
		}
		if response.Msg.State == filesv1.FileState_FILE_STATE_READY {
			ready = response.Msg
			break
		}
		if err := f.worker.Step(f.ctx); err != nil {
			t.Fatal("processing through real dependencies", err)
		}
	}
	if ready == nil {
		t.Fatal("file never became READY")
	}
	foreign, _ := login()
	if _, err := filesv1connect.NewFileServiceClient(foreign, base).GetStatus(f.ctx, browserRequest(&filesv1.GetStatusRequest{FileId: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("foreign account received file state")
	}
	download, err := api.CreateDownload(f.ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: id}))
	if err != nil {
		t.Fatal("download through tunnel", connect.CodeOf(err))
	}
	response, err := client.Get(download.Msg.Url)
	if err != nil {
		t.Fatal("delivery transport")
	}
	clean, readErr := io.ReadAll(io.LimitReader(response.Body, file.MaxSize+1))
	response.Body.Close()
	cleanSum := sha256.Sum256(clean)
	if readErr != nil || response.StatusCode != 200 || !bytes.Equal(cleanSum[:], ready.CleanSha256) {
		t.Fatal("delivery integrity")
	}
	if _, err := api.Delete(f.ctx, browserRequest(&filesv1.DeleteRequest{FileId: id})); err != nil {
		t.Fatal("delete through tunnel", connect.CodeOf(err))
	}
	if _, err := api.CreateDownload(f.ctx, browserRequest(&filesv1.CreateDownloadRequest{FileId: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal("deleted file exposed")
	}
	if _, err := auth.Logout(f.ctx, browserRequest(&authv1.LogoutRequest{})); err != nil {
		t.Fatal("logout", connect.CodeOf(err))
	}
	if _, err := api.GetStatus(f.ctx, browserRequest(&filesv1.GetStatusRequest{FileId: id})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal("logged-out account accessed file")
	}
}
