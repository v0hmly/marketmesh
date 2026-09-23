package podchaos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"sync"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

const fakeInternalGRPCPort = 9443

// LedgerPortForward is one loopback-only forwarding process to an exact,
// ownership-validated fake-internal pod. Close is bounded and idempotent.
type LedgerPortForward struct {
	address   string
	command   *exec.Cmd
	wait      chan error
	closeOnce sync.Once
	closeErr  error
}

// StartLedgerPortForward starts a long-lived forwarding process tied to ctx.
// Callers must keep ctx alive until the final one-shot ledger collection and
// then call Close.
func StartLedgerPortForward(
	ctx context.Context,
	pod PodRef,
	kubectlPath string,
) (*LedgerPortForward, error) {
	if !hasDeadline(ctx) || pod.Deployment != workload.FakeInternalDeployment {
		return nil, fmt.Errorf("%w: ledger port-forward input is invalid", ErrUnsafeState)
	}
	if err := validatePodRef(pod.OwnerRunID, pod); err != nil {
		return nil, err
	}
	if err := validateKubectlPath(kubectlPath); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	return startLedgerPortForward(ctx, pod, kubectlPath)
}

func startLedgerPortForward(
	ctx context.Context,
	pod PodRef,
	kubectlPath string,
) (*LedgerPortForward, error) {
	if kubectlPath == "" {
		return nil, fmt.Errorf("%w: kubectl path is required", ErrInvalidConfiguration)
	}
	output := newPortForwardOutput(fakeInternalGRPCPort)
	// #nosec G204 -- kubectl is an explicit validated executable, no shell is
	// used, and all pod-derived arguments passed the exact ownership validator.
	command := exec.CommandContext(
		ctx,
		kubectlPath,
		"--kubeconfig="+pod.KubeconfigPath,
		"--context="+pod.ContextName,
		"port-forward",
		"--namespace="+pod.Namespace,
		"pod/"+pod.Name,
		"--address=127.0.0.1",
		":"+strconv.Itoa(fakeInternalGRPCPort),
	)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return nil, errors.New("podchaos: starting ledger port-forward")
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	var port int
	select {
	case <-ctx.Done():
		_ = stopCommand(command, wait)
		return nil, ctx.Err()
	case exitErr := <-wait:
		wait <- exitErr
		_ = stopCommand(command, wait)
		return nil, errors.New("podchaos: ledger port-forward exited before readiness")
	case port = <-output.port:
	}
	if output.truncated || port <= 0 || port > 65_535 {
		_ = stopCommand(command, wait)
		return nil, errors.New("podchaos: ledger port-forward readiness is invalid")
	}
	return &LedgerPortForward{
		address: "127.0.0.1:" + strconv.Itoa(port),
		command: command,
		wait:    wait,
	}, nil
}

// Address returns the literal loopback target for grpc.NewClient.
func (forward *LedgerPortForward) Address() string {
	if forward == nil {
		return ""
	}
	return forward.address
}

// Close terminates and reaps kubectl with the package-wide two-phase bound.
func (forward *LedgerPortForward) Close() error {
	if forward == nil {
		return nil
	}
	forward.closeOnce.Do(func() {
		forward.closeErr = stopCommand(forward.command, forward.wait)
	})
	return forward.closeErr
}
