package podchaos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"time"
)

const (
	proxyStartupPoll    = 10 * time.Millisecond
	proxyShutdownWait   = 2 * time.Second
	maxDeleteReplyBytes = 64 * 1024
)

type proxyPodDeleter struct {
	kubectlPath string
}

type deleteOptions struct {
	APIVersion         string              `json:"apiVersion"`
	Kind               string              `json:"kind"`
	GracePeriodSeconds int64               `json:"gracePeriodSeconds"`
	Preconditions      deletePreconditions `json:"preconditions"`
}

type deletePreconditions struct {
	UID string `json:"uid"`
}

func newProxyPodDeleter(kubectlPath string) proxyPodDeleter {
	return proxyPodDeleter{kubectlPath: kubectlPath}
}

func (deleter proxyPodDeleter) DeleteExactPod(
	ctx context.Context,
	pod PodRef,
	gracePeriod time.Duration,
) (resultErr error) {
	if !hasDeadline(ctx) || deleter.kubectlPath == "" ||
		gracePeriod <= 0 || gracePeriod%time.Second != 0 {
		return fmt.Errorf("%w: exact pod deletion input is invalid", ErrUnsafeState)
	}
	if err := validatePodRef(pod.OwnerRunID, pod); err != nil {
		return err
	}

	temporaryDirectory, err := os.MkdirTemp("", "marketmesh-mm32-pod-delete-")
	if err != nil {
		return errors.New("podchaos: creating delete proxy directory")
	}
	defer func() {
		if err := os.RemoveAll(temporaryDirectory); err != nil {
			resultErr = errors.Join(resultErr, errors.New("podchaos: removing delete proxy directory"))
		}
	}()
	socketPath := temporaryDirectory + "/proxy.sock"
	apiPath := "/api/v1/namespaces/" + url.PathEscape(pod.Namespace) +
		"/pods/" + url.PathEscape(pod.Name)

	proxyOutput := &boundedBuffer{remaining: maxKubectlOutputBytes}
	// #nosec G204 -- kubectl is an explicit validated executable, no shell is
	// used, and every pod-derived argument passed exact allowlist validation.
	command := exec.CommandContext(
		ctx,
		deleter.kubectlPath,
		"--kubeconfig="+pod.KubeconfigPath,
		"--context="+pod.ContextName,
		"proxy",
		"--unix-socket="+socketPath,
		"--api-prefix=/",
		"--accept-hosts=^localhost$",
		"--accept-paths=^"+regexp.QuoteMeta(apiPath)+"$",
		"--reject-methods=^(GET|HEAD|POST|PUT|PATCH|OPTIONS|CONNECT|TRACE)$",
		"--reject-paths=^$",
	)
	command.Stdout = proxyOutput
	command.Stderr = proxyOutput
	if err := command.Start(); err != nil {
		return errors.New("podchaos: starting delete proxy")
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	defer func() {
		resultErr = errors.Join(resultErr, stopCommand(command, wait))
	}()
	if err := waitForUnixSocket(ctx, socketPath, wait); err != nil {
		return err
	}

	body, err := json.Marshal(deleteOptions{
		APIVersion:         "v1",
		Kind:               "DeleteOptions",
		GracePeriodSeconds: int64(gracePeriod / time.Second),
		Preconditions:      deletePreconditions{UID: pod.UID},
	})
	if err != nil {
		return errors.New("podchaos: encoding delete options")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodDelete,
		"http://localhost"+apiPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return errors.New("podchaos: creating delete request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	dialer := &net.Dialer{}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(callCtx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(callCtx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: transport}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("podchaos: exact pod deletion failed")
	}
	reply, readErr := io.ReadAll(io.LimitReader(response.Body, maxDeleteReplyBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || len(reply) > maxDeleteReplyBytes {
		return errors.New("podchaos: delete response exceeded bounds")
	}
	if closeErr != nil {
		return errors.New("podchaos: closing delete response")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return errors.New("podchaos: kubernetes rejected exact pod deletion")
	}

	return nil
}

func waitForUnixSocket(ctx context.Context, socketPath string, wait chan error) error {
	ticker := time.NewTicker(proxyStartupPoll)
	defer ticker.Stop()
	for {
		info, err := os.Stat(socketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case exitErr := <-wait:
			wait <- exitErr
			return errors.New("podchaos: delete proxy exited before readiness")
		case <-ticker.C:
		}
	}
}

func stopCommand(command *exec.Cmd, wait chan error) error {
	if command == nil || command.Process == nil {
		return nil
	}
	select {
	case <-wait:
		return nil
	default:
	}
	var resultErr error
	if err := command.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		resultErr = errors.New("podchaos: interrupting kubectl process")
	}
	timer := time.NewTimer(proxyShutdownWait)
	defer timer.Stop()
	select {
	case <-wait:
		return resultErr
	case <-timer.C:
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		resultErr = errors.Join(resultErr, errors.New("podchaos: killing kubectl process"))
	}

	killTimer := time.NewTimer(proxyShutdownWait)
	defer killTimer.Stop()
	select {
	case <-wait:
		return resultErr
	case <-killTimer.C:
		return errors.Join(resultErr, errors.New("podchaos: waiting for kubectl process shutdown"))
	}
}
