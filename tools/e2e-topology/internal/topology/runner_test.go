package topology

import (
	"errors"
	"strings"
	"testing"
)

func TestCommandEnvironmentReplacesSensitiveSelectors(t *testing.T) {
	t.Setenv("KUBECONFIG", "/unexpected/user-config")
	t.Setenv("DOCKER_CONTEXT", "unexpected")
	t.Setenv("KIND_EXPERIMENTAL_DOCKER_NETWORK", "unexpected")

	environment := commandEnvironment([]string{
		"KUBECONFIG=/owned/config",
		"DOCKER_CONTEXT=orbstack",
		"KIND_EXPERIMENTAL_DOCKER_NETWORK=mm28-dc-a-dmz",
	})
	joined := strings.Join(environment, "\n")
	for _, forbidden := range []string{
		"/unexpected/user-config",
		"DOCKER_CONTEXT=unexpected",
		"KIND_EXPERIMENTAL_DOCKER_NETWORK=unexpected",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("environment contains %q", forbidden)
		}
	}
	for _, expected := range []string{
		"KUBECONFIG=/owned/config",
		"DOCKER_CONTEXT=orbstack",
		"KIND_EXPERIMENTAL_DOCKER_NETWORK=mm28-dc-a-dmz",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("environment does not contain %q", expected)
		}
	}
}

func TestLimitedBufferRejectsOversizedOutput(t *testing.T) {
	t.Parallel()

	buffer := &limitedBuffer{remaining: 3}
	if _, err := buffer.Write([]byte("abc")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := buffer.Write([]byte("d")); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("Write() error = %v, want ErrOutputLimit", err)
	}
	if got := buffer.String(); got != "abc" {
		t.Errorf("buffer = %q, want abc", got)
	}
}
