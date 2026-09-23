package podchaos

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLedgerPortForwardUsesExactOwnedPodAndCloses(t *testing.T) {
	temporaryDirectory := t.TempDir()
	wrapperPath := filepath.Join(temporaryDirectory, "kubectl")
	wrapper := []byte("#!/bin/sh\nexec \"$MM32_HELPER_TEST_BINARY\" -test.run=TestLedgerPortForwardHelperProcess -- \"$@\"\n")
	if err := os.WriteFile(wrapperPath, wrapper, 0o700); err != nil {
		t.Fatalf("os.WriteFile(wrapper) error = %v", err)
	}
	capturePath := filepath.Join(temporaryDirectory, "capture.json")
	t.Setenv("MM32_LEDGER_FORWARD_HELPER", "1")
	t.Setenv("MM32_HELPER_TEST_BINARY", os.Args[0])
	t.Setenv("MM32_HELPER_CAPTURE", capturePath)
	pod := PodRef{
		KubeconfigPath: "/tmp/mm32-ledger-kubeconfig",
		ContextName:    "kind-mm32-ledger-internal",
		Namespace:      "marketmesh-e2e-tunnel",
		Deployment:     "mm29-fake-internal",
		Name:           "mm29-fake-internal-exact",
		UID:            "pod-uid-ledger",
		OwnerRunID:     "mm32-ledger",
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	forward, err := startLedgerPortForward(ctx, pod, wrapperPath)
	if err != nil {
		t.Fatalf("startLedgerPortForward() error = %v", err)
	}
	if host, _, err := net.SplitHostPort(forward.Address()); err != nil || host != "127.0.0.1" {
		t.Fatalf("Address() = %q, err = %v", forward.Address(), err)
	}
	if err := forward.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := forward.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
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
		":9443",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("port-forward arguments %q do not contain %q", joined, required)
		}
	}
	if strings.Contains(joined, "service/") || strings.Contains(joined, "0.0.0.0") {
		t.Fatalf("unsafe port-forward arguments = %q", joined)
	}
}

func TestLedgerPortForwardHelperProcess(t *testing.T) {
	if os.Getenv("MM32_LEDGER_FORWARD_HELPER") != "1" {
		return
	}
	arguments := argumentsAfterSeparator(os.Args)
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("json.Marshal(arguments) error = %v", err)
	}
	if err := os.WriteFile(os.Getenv("MM32_HELPER_CAPTURE"), encoded, 0o600); err != nil {
		t.Fatalf("os.WriteFile(capture) error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(tcp) error = %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("listener.Close() error = %v", err)
		}
	})
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := fmt.Fprintf(os.Stdout, "Forwarding from 127.0.0.1:%d -> 9443\n", port); err != nil {
		t.Fatalf("fmt.Fprintf(stdout) error = %v", err)
	}
	select {}
}
