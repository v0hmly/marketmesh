package dcfailover

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/frontdoor"
)

var _ FrontDoor = (*frontdoor.FrontDoor)(nil)

func TestRunnerUsesPublicMM30FrontDoorHealthContract(t *testing.T) {
	t.Parallel()

	backendA := newReadyBackend(t)
	backendB := newReadyBackend(t)
	publicFrontDoor, err := frontdoor.New(frontdoor.Config{
		DCATarget:           backendA.URL,
		DCBTarget:           backendB.URL,
		HealthCheckInterval: time.Second,
		HealthCheckTimeout:  100 * time.Millisecond,
		FailbackWarmup:      time.Second,
	})
	if err != nil {
		t.Fatalf("frontdoor.New() error = %v", err)
	}

	adapters := newFakeAdapters()
	dependencies := testDependencies(adapters)
	dependencies.FrontDoor = publicFrontDoor
	runner, err := New(testConfig(), dependencies)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func newReadyBackend(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" || request.Method != http.MethodGet {
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return server
}
