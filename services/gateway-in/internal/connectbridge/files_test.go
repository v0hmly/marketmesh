package connectbridge

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

type filesInvoker struct {
	fakeInvoker
	calls int
}

func (f *filesInvoker) Invoke(ctx context.Context, call tunnel.Call) (tunnel.Response, error) {
	f.calls++
	return f.fakeInvoker.Invoke(ctx, call)
}
func (*filesInvoker) RoutePolicy(route contractv1.RouteId) (tunnel.RoutePolicy, bool) {
	return tunnel.RoutePolicy{MaxRequestBytes: 16384, MaxResponseBytes: 16384}, route >= 300 && route <= 304
}
func filesHTTP(t *testing.T, invoker *filesInvoker, method, path, body string, secure bool, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := NewFileHandler(invoker)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if secure {
		request.TLS = &tls.ConnectionState{}
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable response")
	}
	return result
}
func TestFilesFixedRoutesAndOpaqueContext(t *testing.T) {
	for _, test := range []struct {
		method, body      string
		route             contractv1.RouteId
		request, response proto.Message
	}{
		{"GetStatus", `{"subjectId":"forged","context":{"cookie":["evil"]}}`, 302, &gatewayv1.BrowserFileGetStatusRequest{}, &gatewayv1.BrowserFileGetStatusResponse{Response: &filesv1.GetStatusResponse{State: filesv1.FileState_FILE_STATE_SCANNING}}},
		{"Delete", `{"fileId":"AQAAAAAAAAAAAAAAAAAAAA=="}`, 304, &gatewayv1.BrowserFileDeleteRequest{}, &gatewayv1.BrowserFileDeleteResponse{Response: &filesv1.DeleteResponse{}}},
	} {
		t.Run(test.method, func(t *testing.T) {
			payload, err := proto.Marshal(test.response)
			if err != nil {
				t.Fatal(err)
			}
			invoker := &filesInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}}}
			headers := http.Header{"Cookie": {"a=opaque==", "b=second"}, "Origin": {"https://shop.test"}, "Sec-Fetch-Site": {"same-origin"}, "Authorization": {"Bearer private"}, "X-Session-Assertion": {"forged"}}
			result := filesHTTP(t, invoker, "POST", "/files.v1.FileService/"+test.method, test.body, true, headers)
			if result.Code != 200 {
				t.Fatal(result.Code, result.Body.String())
			}
			call := invoker.lastCall()
			if call.Route != test.route || invoker.calls != 1 || len(call.Metadata) != 0 || len(call.IdempotencyKey) != 0 {
				t.Fatal("unexpected call")
			}
			if err := proto.Unmarshal(call.Payload, test.request); err != nil {
				t.Fatal(err)
			}
			browser := test.request.ProtoReflect().Get(test.request.ProtoReflect().Descriptor().Fields().ByName("context")).Message().Interface().(*authv1.BrowserContext)
			if !proto.Equal(browser, &authv1.BrowserContext{Cookie: headers.Values("Cookie"), Origin: headers.Values("Origin"), SecFetchSite: headers.Values("Sec-Fetch-Site")}) {
				t.Fatal("context changed")
			}
			if strings.Contains(string(call.Payload), "private") || strings.Contains(string(call.Payload), "forged") {
				t.Fatal("client assertion forwarded")
			}
		})
	}
}
func TestFilesRejectsBeforeInvoke(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		secure                   bool
		headers                  http.Header
	}{
		{"HTTP", "POST", "/files.v1.FileService/GetStatus", "{}", false, http.Header{"X-Forwarded-Proto": {"https"}}},
		{"GET", "GET", "/files.v1.FileService/GetStatus", "{}", true, nil},
		{"private RPC", "POST", "/gateway.v1.FileBrowserService/BrowserFileGetStatus", "{}", true, nil},
		{"malformed", "POST", "/files.v1.FileService/GetStatus", "{", true, nil},
		{"body bound", "POST", "/files.v1.FileService/Delete", strings.Repeat(" ", 16385), true, nil},
		{"cookie bound", "POST", "/files.v1.FileService/GetStatus", "{}", true, http.Header{"Cookie": {strings.Repeat("x", 8193)}}},
		{"context injection", "POST", "/files.v1.FileService/GetStatus", "{}", true, http.Header{"Origin": {"https://a\r\nx:y"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invoker := new(filesInvoker)
			result := filesHTTP(t, invoker, test.method, test.path, test.body, test.secure, test.headers)
			if result.Code == 200 || invoker.calls != 0 {
				t.Fatal("request accepted", result.Code)
			}
		})
	}
}
func TestFilesFailureEnvelopeAndSafeErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *gatewayv1.BrowserFileGetStatusResponse
		payload  []byte
		err      error
		status   int
		reason   string
	}{
		{name: "preparing", response: &gatewayv1.BrowserFileGetStatusResponse{Failure: 1}, status: 404, reason: "not_found"},
		{name: "conflict", response: &gatewayv1.BrowserFileGetStatusResponse{Failure: 2}, status: 409, reason: "aborted"},
		{name: "unknown", response: &gatewayv1.BrowserFileGetStatusResponse{Failure: 99}, status: 500},
		{name: "ambiguous", response: &gatewayv1.BrowserFileGetStatusResponse{Failure: 1, Response: &filesv1.GetStatusResponse{}}, status: 500},
		{name: "missing", response: &gatewayv1.BrowserFileGetStatusResponse{}, status: 500},
		{name: "malformed", payload: []byte{255}, status: 500},
		{name: "oversized", payload: make([]byte, 16385), status: 429},
		{name: "private error", err: errors.New("secret https://storage.internal/?X-Amz-Signature=secret"), status: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := test.payload
			if test.response != nil {
				var err error
				payload, err = proto.Marshal(test.response)
				if err != nil {
					t.Fatal(err)
				}
			}
			invoker := &filesInvoker{fakeInvoker: fakeInvoker{response: tunnel.Response{Payload: payload}, err: test.err}}
			result := filesHTTP(t, invoker, "POST", "/files.v1.FileService/GetStatus", "{}", true, nil)
			if result.Code != test.status {
				t.Fatal(result.Code, result.Body.String())
			}
			// ErrorInfo is a protobuf detail encoded as base64 in Connect JSON.
			if test.reason == "aborted" && !strings.Contains(result.Body.String(), test.reason) {
				t.Fatal(result.Body.String())
			}
			if test.reason == "PROFILE_NOT_READY" && !strings.Contains(result.Body.String(), "google.rpc.ErrorInfo") {
				t.Fatal(result.Body.String())
			}
			if strings.Contains(result.Body.String(), "secret") || strings.Contains(result.Body.String(), "X-Amz-Signature") {
				t.Fatal("private error leaked")
			}
		})
	}
}
