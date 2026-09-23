package topology

import (
	"errors"
	"strings"
	"testing"
)

func TestCommandEnvironmentReplacesSensitiveSelectors(t *testing.T) {
	t.Setenv("KUBECONFIG", "/unexpected/user-config")

	environment := commandEnvironment([]string{
		"KUBECONFIG=/owned/config",
	})
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "/unexpected/user-config") {
		t.Errorf("environment contains the unexpected KUBECONFIG")
	}
	if !strings.Contains(joined, "KUBECONFIG=/owned/config") {
		t.Errorf("environment does not contain the owned KUBECONFIG")
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
