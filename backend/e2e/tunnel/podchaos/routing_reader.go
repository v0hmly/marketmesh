package podchaos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
)

const gatewayInHTTPPort = 8080

var forwardingPortPattern = regexp.MustCompile(
	`Forwarding from 127\.0\.0\.1:([0-9]{1,5}) -> ([0-9]{1,5})`,
)

// KubectlRoutingReader uses one short-lived loopback port-forward to an exact
// gateway-in pod for every snapshot. It never uses a Service or NodePort.
type KubectlRoutingReader struct {
	kubectlPath string
}

// NewKubectlRoutingReader creates strict per-pod routing reads with one exact
// operator-supplied kubectl binary; ambient PATH is never consulted.
func NewKubectlRoutingReader(path string) (*KubectlRoutingReader, error) {
	if err := validateKubectlPath(path); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfiguration, err)
	}
	return &KubectlRoutingReader{kubectlPath: path}, nil
}

// ReadRoutingSnapshot implements RoutingReader.
func (reader *KubectlRoutingReader) ReadRoutingSnapshot(
	ctx context.Context,
	pod PodRef,
) (result RoutingSnapshot, resultErr error) {
	if !hasDeadline(ctx) || reader == nil || reader.kubectlPath == "" {
		return RoutingSnapshot{}, fmt.Errorf("%w: routing read input is invalid", ErrUnsafeState)
	}
	if err := validatePodRef(pod.OwnerRunID, pod); err != nil {
		return RoutingSnapshot{}, err
	}

	output := newPortForwardOutput(gatewayInHTTPPort)
	// #nosec G204 -- kubectl is an explicit validated executable, no shell is
	// used, and every pod-derived argument passed exact allowlist validation.
	command := exec.CommandContext(
		ctx,
		reader.kubectlPath,
		"--kubeconfig="+pod.KubeconfigPath,
		"--context="+pod.ContextName,
		"port-forward",
		"--namespace="+pod.Namespace,
		"pod/"+pod.Name,
		"--address=127.0.0.1",
		":"+strconv.Itoa(gatewayInHTTPPort),
	)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return RoutingSnapshot{}, errors.New("podchaos: starting routing port-forward")
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	defer func() {
		resultErr = errors.Join(resultErr, stopCommand(command, wait))
	}()

	var localPort int
	select {
	case <-ctx.Done():
		return RoutingSnapshot{}, ctx.Err()
	case exitErr := <-wait:
		wait <- exitErr
		return RoutingSnapshot{}, errors.New("podchaos: routing port-forward exited before readiness")
	case localPort = <-output.port:
	}
	if localPort <= 0 || localPort > 65_535 {
		return RoutingSnapshot{}, errors.New("podchaos: routing port-forward returned invalid port")
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://127.0.0.1:"+strconv.Itoa(localPort)+"/_e2e/tunnel-routing-snapshot",
		http.NoBody,
	)
	if err != nil {
		return RoutingSnapshot{}, errors.New("podchaos: creating routing request")
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
	}}
	response, err := client.Do(request)
	if err != nil {
		return RoutingSnapshot{}, errors.New("podchaos: routing request failed")
	}
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "application/json" {
		if err := response.Body.Close(); err != nil {
			return RoutingSnapshot{}, errors.New("podchaos: closing rejected routing response")
		}
		return RoutingSnapshot{}, errors.New("podchaos: routing endpoint rejected request")
	}
	snapshot, err := DecodeRoutingSnapshot(response.Body)
	closeErr := response.Body.Close()
	if err != nil {
		return RoutingSnapshot{}, err
	}
	if closeErr != nil {
		return RoutingSnapshot{}, errors.New("podchaos: closing routing response")
	}
	if snapshot.GatewayInInstance != pod.Name {
		return RoutingSnapshot{}, fmt.Errorf("%w: routing snapshot pod mismatch", ErrUnsafeState)
	}

	return snapshot, nil
}

type portForwardOutput struct {
	mu         sync.Mutex
	content    []byte
	port       chan int
	isReady    bool
	truncated  bool
	remotePort int
}

func newPortForwardOutput(remotePort int) *portForwardOutput {
	return &portForwardOutput{
		content:    make([]byte, 0, 1024),
		port:       make(chan int, 1),
		remotePort: remotePort,
	}
}

func (output *portForwardOutput) Write(content []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	original := len(content)
	remaining := maxKubectlOutputBytes - len(output.content)
	if len(content) > remaining {
		content = content[:max(remaining, 0)]
		output.truncated = true
	}
	output.content = append(output.content, content...)
	if output.isReady {
		return original, nil
	}
	match := forwardingPortPattern.FindSubmatch(output.content)
	if len(match) != 3 {
		return original, nil
	}
	port, err := strconv.Atoi(string(match[1]))
	remotePort, remoteErr := strconv.Atoi(string(match[2]))
	if err != nil || remoteErr != nil || port <= 0 || port > 65_535 ||
		remotePort != output.remotePort {
		return original, nil
	}
	output.isReady = true
	output.port <- port
	return original, nil
}
