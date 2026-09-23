package main

import (
	"sync/atomic"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/podchaos"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

func TestRuntimeResourcesCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	resources := &runtimeResources{cancel: func() { calls.Add(1) }}
	if err := resources.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := resources.Close(); err != nil {
		t.Fatalf("Close() second error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", calls.Load())
	}
	if err := (*runtimeResources)(nil).Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
}

func TestProbeDataCenterMapping(t *testing.T) {
	t.Parallel()

	if probeDataCenter(podchaos.DCA) != probe.DataCenterA ||
		probeDataCenter(podchaos.DCB) != probe.DataCenterB ||
		probeDataCenter(podchaos.DCUnknown) != probe.DataCenterUnknown {
		t.Fatal("probeDataCenter() mapping is incomplete")
	}
}
