//go:build integration

package spec_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestWriteRunRoundTripsAtExactDecoderCapacity(t *testing.T) {
	run := spec.Run{SchemaVersion: spec.RunSchemaVersion}
	var baseline bytes.Buffer
	if err := spec.WriteRun(&baseline, run); err != nil {
		t.Fatalf("WriteRun(baseline) error = %v", err)
	}

	available := spec.MaxRunJSONBytes - int64(baseline.Len())
	escapedCharacters := available / 6
	plainCharacters := available % 6
	run.RunID = strings.Repeat("<", int(escapedCharacters)) +
		strings.Repeat("a", int(plainCharacters))

	var encoded bytes.Buffer
	if err := spec.WriteRun(&encoded, run); err != nil {
		t.Fatalf("WriteRun(boundary) error = %v", err)
	}
	if size := int64(encoded.Len()); size != spec.MaxRunJSONBytes {
		t.Fatalf("encoded size = %d, want %d", size, spec.MaxRunJSONBytes)
	}
	if _, err := spec.DecodeRun(bytes.NewReader(encoded.Bytes())); err != nil {
		t.Fatalf("DecodeRun(boundary) error = %v", err)
	}

	run.RunID += "a"
	var oversized bytes.Buffer
	if err := spec.WriteRun(&oversized, run); err == nil {
		t.Fatal("WriteRun(oversized) error = nil")
	}
	if oversized.Len() != 0 {
		t.Fatalf("oversized writer bytes = %d, want 0", oversized.Len())
	}
}
