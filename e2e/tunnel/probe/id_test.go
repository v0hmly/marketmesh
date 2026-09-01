package probe

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

func TestRandomIDGeneratorProducesUniqueSafeIDs(t *testing.T) {
	t.Parallel()

	generator, err := newRandomIDGenerator(strings.NewReader(strings.Repeat("a", randomPrefixSize)))
	if err != nil {
		t.Fatalf("newRandomIDGenerator() error = %v", err)
	}

	const count = 100
	ids := make(chan string, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			ids <- generator.Next()
		})
	}
	workers.Wait()
	close(ids)

	unique := make(map[string]struct{}, count)
	for id := range ids {
		if !validateRequestID(id) {
			t.Fatalf("generated unsafe request id %q", id)
		}
		if _, exists := unique[id]; exists {
			t.Fatalf("generated duplicate request id %q", id)
		}
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != requestIDSize {
			t.Fatalf("generated request id %q does not decode to %d bytes", id, requestIDSize)
		}
		unique[id] = struct{}{}
	}
}

func TestRandomIDGeneratorRejectsEntropyFailure(t *testing.T) {
	t.Parallel()

	_, err := newRandomIDGenerator(strings.NewReader("short"))
	if err == nil {
		t.Fatal("newRandomIDGenerator() error = nil, want entropy error")
	}
}
