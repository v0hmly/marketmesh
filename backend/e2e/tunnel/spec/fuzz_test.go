package spec_test

import (
	"bytes"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func FuzzDecodeScenarioNeverPanics(f *testing.F) {
	f.Add([]byte(`{"schema_version":"marketmesh.tunnel.slo.scenario/v1"}`))
	f.Add([]byte(`{"schema_version":"x","schema_version":"y"}`))
	f.Add([]byte(`[]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = spec.DecodeScenario(bytes.NewReader(data))
	})
}

func FuzzDecodeRunNeverPanics(f *testing.F) {
	f.Add([]byte(`{"schema_version":"marketmesh.tunnel.slo.run/v1"}`))
	f.Add([]byte(`{"requests":[{"attempts":[{}]}]}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = spec.DecodeRun(bytes.NewReader(data))
	})
}
