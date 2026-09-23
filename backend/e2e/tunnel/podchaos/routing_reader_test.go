package podchaos

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKubectlRoutingReaderUsesExactPodPortForward(t *testing.T) {
	temporaryDirectory := t.TempDir()
	wrapperPath := filepath.Join(temporaryDirectory, "kubectl")
	wrapper := []byte("#!/bin/sh\nexec \"$MM32_HELPER_TEST_BINARY\" -test.run=TestKubectlPortForwardHelperProcess -- \"$@\"\n")
	if err := os.WriteFile(wrapperPath, wrapper, 0o700); err != nil {
		t.Fatalf("os.WriteFile(wrapper) error = %v", err)
	}
	capturePath := filepath.Join(temporaryDirectory, "capture.json")
	t.Setenv("MM32_PORT_FORWARD_HELPER", "1")
	t.Setenv("MM32_HELPER_TEST_BINARY", os.Args[0])
	t.Setenv("MM32_HELPER_CAPTURE", capturePath)

	pod := PodRef{
		KubeconfigPath: "/tmp/mm32-kubeconfig",
		ContextName:    "kind-mm32-a-dmz",
		Namespace:      "marketmesh-e2e-tunnel",
		Deployment:     "mm29-gateway-in",
		Name:           "mm29-gateway-in-abcde",
		UID:            "pod-uid-exact",
		OwnerRunID:     "mm32-routing",
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := (&KubectlRoutingReader{kubectlPath: wrapperPath}).ReadRoutingSnapshot(ctx, pod)
	if err != nil {
		t.Fatalf("ReadRoutingSnapshot() error = %v", err)
	}
	if snapshot.GatewayInInstance != pod.Name {
		t.Fatalf("gateway instance = %q", snapshot.GatewayInInstance)
	}

	encoded, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("os.ReadFile(capture) error = %v", err)
	}
	var arguments []string
	if err := json.Unmarshal(encoded, &arguments); err != nil {
		t.Fatalf("json.Unmarshal(capture) error = %v", err)
	}
	joined := strings.Join(arguments, " ")
	for _, required := range []string{
		"--kubeconfig=" + pod.KubeconfigPath,
		"--context=" + pod.ContextName,
		"port-forward",
		"--namespace=" + pod.Namespace,
		"pod/" + pod.Name,
		"--address=127.0.0.1",
		":8080",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("port-forward arguments %q do not contain %q", joined, required)
		}
	}
	if strings.Contains(joined, "service/") || strings.Contains(joined, "--address=0.0.0.0") {
		t.Fatalf("unsafe port-forward arguments = %q", joined)
	}
}

func TestKubectlPortForwardHelperProcess(t *testing.T) {
	if os.Getenv("MM32_PORT_FORWARD_HELPER") != "1" {
		return
	}
	arguments := argumentsAfterSeparator(os.Args)
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("json.Marshal(arguments) error = %v", err)
	}
	if err := os.WriteFile(
		os.Getenv("MM32_HELPER_CAPTURE"),
		encodedArguments,
		0o600,
	); err != nil {
		t.Fatalf("os.WriteFile(capture) error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp) error = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := fmt.Fprintf(os.Stdout, "Forwarding from 127.0.0.1:%d -> 8080\n", port); err != nil {
		t.Fatalf("fmt.Fprintf(stdout) error = %v", err)
	}

	snapshot := routingSnapshotForGateway(
		"mm29-gateway-in-abcde",
		"dc-a",
		[]RoutingTunnelSnapshot{{
			InstanceID: "11111111111111111111111111111111",
			DataCenter: "dc-a", State: "ready",
		}},
	)
	requestDone := make(chan struct{})
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet ||
				request.URL.Path != "/_e2e/tunnel-routing-snapshot" {
				http.NotFound(response, request)
				return
			}
			response.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(response).Encode(snapshot); err != nil {
				t.Errorf("json.Encode(snapshot) error = %v", err)
			}
			close(requestDone)
		}),
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()
	select {
	case <-requestDone:
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
