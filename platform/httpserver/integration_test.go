package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestListenerIntegrationPropagatesClientCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	canceled := make(chan error, 1)
	config := validTestConfig(t, &bytes.Buffer{})
	config.Handler = http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		canceled <- request.Context().Err()
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := newLoopbackListener(t)
	component, err := Component("http", server, listener)
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- component.Run(context.Background())
	}()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://"+listener.Addr().String()+"/cancel",
		nil,
	)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	clientResult := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		clientResult <- requestErr
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not start")
	}
	cancelRequest()

	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not observe client cancellation")
	}
	select {
	case <-clientResult:
	case <-time.After(time.Second):
		t.Fatal("HTTP client did not finish after cancellation")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := component.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Component.Shutdown() error = %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Component.Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Component.Run() did not finish after shutdown")
	}
}

func TestListenerIntegrationEnforcesRequestDeadline(t *testing.T) {
	t.Parallel()

	deadlineResult := make(chan error, 1)
	config := validTestConfig(t, &bytes.Buffer{})
	config.RequestTimeout = 30 * time.Millisecond
	config.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		deadlineResult <- request.Context().Err()
		response.WriteHeader(http.StatusGatewayTimeout)
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := newLoopbackListener(t)
	component, err := Component("http", server, listener)
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- component.Run(context.Background())
	}()

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/deadline")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.StatusCode)
	}
	select {
	case err := <-deadlineResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler context error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not observe request deadline")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := component.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Component.Shutdown() error = %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Component.Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Component.Run() did not finish after shutdown")
	}
}

func TestListenerIntegrationForcesCloseAtShutdownDeadline(t *testing.T) {
	t.Parallel()

	const attempts = 5
	for attempt := range attempts {
		t.Run(fmt.Sprintf("attempt-%d", attempt+1), testForcedCloseAtShutdownDeadline)
	}
}

func testForcedCloseAtShutdownDeadline(t *testing.T) {
	t.Helper()

	started := make(chan struct{})
	handlerDone := make(chan error, 1)
	config := validTestConfig(t, &bytes.Buffer{})
	config.RequestTimeout = 5 * time.Second
	config.Handler = http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		handlerDone <- request.Context().Err()
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := newLoopbackListener(t)
	component, err := Component("http", server, listener)
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- component.Run(context.Background())
	}()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, _ := http.Get("http://" + listener.Addr().String() + "/shutdown")
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not start")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShutdown()
	startedShutdown := time.Now()
	shutdownErr := component.Shutdown(shutdownCtx)
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("Component.Shutdown() error = %v, want context.DeadlineExceeded", shutdownErr)
	}
	if elapsed := time.Since(startedShutdown); elapsed > time.Second {
		t.Fatalf("Component.Shutdown() elapsed = %v, want bounded shutdown", elapsed)
	}

	select {
	case err := <-handlerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("forced-close handler error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("forced close did not cancel active handler")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Component.Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Component.Run() did not finish after forced close")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP client goroutine remained after forced close")
	}
}

func TestComponentRejectsInvalidServerAndTypedNilListener(t *testing.T) {
	t.Parallel()

	config := validTestConfig(t, &bytes.Buffer{})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var listener *typedNilListener

	if _, err := Component("http", nil, listener); err == nil {
		t.Fatal("Component(nil server) error = nil")
	}
	if _, err := Component("http", &http.Server{}, listener); err == nil {
		t.Fatal("Component(nil handler) error = nil")
	}
	if _, err := Component("http", server, listener); err == nil {
		t.Fatal("Component(typed nil listener) error = nil")
	}
}

func newLoopbackListener(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	return listener
}

type typedNilListener struct{}

func (*typedNilListener) Accept() (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (*typedNilListener) Close() error {
	return nil
}

func (*typedNilListener) Addr() net.Addr {
	return nil
}
