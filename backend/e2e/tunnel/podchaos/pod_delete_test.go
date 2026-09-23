package podchaos

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProxyPodDeleterSendsUIDPreconditionToExactPath(t *testing.T) {
	temporaryDirectory := t.TempDir()
	wrapperPath := filepath.Join(temporaryDirectory, "kubectl")
	wrapper := []byte("#!/bin/sh\nexec \"$MM32_HELPER_TEST_BINARY\" -test.run=TestKubectlProxyHelperProcess -- \"$@\"\n")
	if err := os.WriteFile(wrapperPath, wrapper, 0o700); err != nil {
		t.Fatalf("os.WriteFile(wrapper) error = %v", err)
	}
	capturePath := filepath.Join(temporaryDirectory, "capture.json")
	t.Setenv("MM32_HELPER_PROCESS", "1")
	t.Setenv("MM32_HELPER_TEST_BINARY", os.Args[0])
	t.Setenv("MM32_HELPER_CAPTURE", capturePath)

	pod := PodRef{
		KubeconfigPath: "/tmp/mm32-kubeconfig",
		ContextName:    "kind-mm32-a-dmz",
		Namespace:      "marketmesh-e2e-tunnel",
		Deployment:     "mm29-gateway-in",
		Name:           "mm29-gateway-in-abcde",
		UID:            "pod-uid-exact",
		OwnerRunID:     "mm32-delete",
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := newProxyPodDeleter(wrapperPath).DeleteExactPod(
		ctx,
		pod,
		30*time.Second,
	); err != nil {
		t.Fatalf("DeleteExactPod() error = %v", err)
	}

	encoded, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("os.ReadFile(capture) error = %v", err)
	}
	var capture deleteCapture
	if err := json.Unmarshal(encoded, &capture); err != nil {
		t.Fatalf("json.Unmarshal(capture) error = %v", err)
	}
	if capture.Method != http.MethodDelete ||
		capture.Path != "/api/v1/namespaces/marketmesh-e2e-tunnel/pods/mm29-gateway-in-abcde" ||
		capture.ContentType != "application/json" {
		t.Fatalf("capture request = %+v", capture)
	}
	var options deleteOptions
	if err := json.Unmarshal(capture.Body, &options); err != nil {
		t.Fatalf("json.Unmarshal(delete options) error = %v", err)
	}
	if options.GracePeriodSeconds != 30 || options.Preconditions.UID != pod.UID {
		t.Fatalf("delete options = %+v", options)
	}
	joined := strings.Join(capture.Arguments, " ")
	for _, required := range []string{
		"--kubeconfig=" + pod.KubeconfigPath,
		"--context=" + pod.ContextName,
		"proxy",
		"--unix-socket=",
		"--accept-paths=^/api/v1/namespaces/marketmesh-e2e-tunnel/pods/mm29-gateway-in-abcde$",
		"--reject-methods=^(GET|HEAD|POST|PUT|PATCH|OPTIONS|CONNECT|TRACE)$",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("proxy arguments %q do not contain %q", joined, required)
		}
	}
	if strings.Contains(joined, "--disable-filter=true") {
		t.Fatalf("proxy arguments disable request filtering: %q", joined)
	}
}

func TestKubectlProxyHelperProcess(t *testing.T) {
	if os.Getenv("MM32_HELPER_PROCESS") != "1" {
		return
	}
	arguments := argumentsAfterSeparator(os.Args)
	socketPath := argumentValue(arguments, "--unix-socket=")
	if socketPath == "" {
		t.Fatal("helper process did not receive unix socket")
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix) error = %v", err)
	}
	captured := make(chan struct{})
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			body, readErr := io.ReadAll(io.LimitReader(request.Body, maxDeleteReplyBytes+1))
			if readErr != nil {
				t.Errorf("io.ReadAll(request) error = %v", readErr)
			}
			capture := deleteCapture{
				Arguments: slices.Clone(arguments),
				Method:    request.Method, Path: request.URL.Path,
				ContentType: request.Header.Get("Content-Type"), Body: body,
			}
			encoded, encodeErr := json.Marshal(capture)
			if encodeErr != nil {
				t.Errorf("json.Marshal(capture) error = %v", encodeErr)
			} else if writeErr := os.WriteFile(
				os.Getenv("MM32_HELPER_CAPTURE"),
				encoded,
				0o600,
			); writeErr != nil {
				t.Errorf("os.WriteFile(capture) error = %v", writeErr)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_, _ = response.Write([]byte(`{"kind":"Pod"}`))
			close(captured)
		}),
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()
	select {
	case <-captured:
	case <-time.After(3 * time.Second):
		t.Fatal("helper process did not receive request")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("server.Shutdown() error = %v", err)
	}
	if err := <-serverDone; err != nil && err != http.ErrServerClosed {
		t.Fatalf("server.Serve() error = %v", err)
	}
}

type deleteCapture struct {
	Arguments   []string        `json:"arguments"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	ContentType string          `json:"content_type"`
	Body        json.RawMessage `json:"body"`
}

func argumentsAfterSeparator(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" {
			return slices.Clone(arguments[index+1:])
		}
	}
	return []string{}
}

func argumentValue(arguments []string, prefix string) string {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimPrefix(argument, prefix)
		}
	}
	return ""
}
