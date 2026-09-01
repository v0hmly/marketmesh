package networkchaos

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateResourcesPassesBoundedLedger(t *testing.T) {
	t.Parallel()

	result, err := EvaluateResources(resourceLimits(), []ResourceSample{
		resourceSample(0, 100, 1_000, 1, 1, 4),
		resourceSample(time.Minute, 105, 1_100, 2, 1, 8),
		resourceSample(2*time.Minute, 102, 1_050, 1, 1, 2),
	})
	if err != nil {
		t.Fatalf("EvaluateResources() error = %v", err)
	}
	if err := result.Gate(); err != nil {
		t.Fatalf("Gate() error = %v", err)
	}
	if !result.Passed || result.PeakGoroutines != 105 || result.PeakHeapBytes != 1_100 {
		t.Fatalf("EvaluateResources() result = %+v", result)
	}
	if result.PeakQueueDepth[TrafficClassRealtime] != 8 {
		t.Fatalf("realtime peak = %d, want 8", result.PeakQueueDepth[TrafficClassRealtime])
	}
}

func TestEvaluateResourcesPreservesEveryObservedViolation(t *testing.T) {
	t.Parallel()

	result, err := EvaluateResources(resourceLimits(), []ResourceSample{
		resourceSample(0, 100, 1_000, 1, 1, 4),
		resourceSample(time.Minute, 121, 1_000, 1, 1, 4),
		resourceSample(2*time.Minute, 100, 1_300, 1, 1, 4),
		resourceSample(3*time.Minute, 100, 1_000, 9, 1, 4),
		resourceSample(4*time.Minute, 100, 1_000, 1, 1, 4),
	})
	if err != nil {
		t.Fatalf("EvaluateResources() error = %v", err)
	}
	if result.Passed {
		t.Fatal("EvaluateResources() passed after transient violations")
	}
	gateErr := result.Gate()
	if gateErr == nil {
		t.Fatal("Gate() error = nil, want failure")
	}
	for _, expected := range []string{"goroutines", "heap bytes", "control queue"} {
		if !strings.Contains(gateErr.Error(), expected) {
			t.Fatalf("Gate() error = %v, want %q", gateErr, expected)
		}
	}
}

func TestEvaluateResourcesRejectsUnknownIntervalsAndClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limits  ResourceLimits
		samples []ResourceSample
		wantErr string
	}{
		{
			name: "missing auth queue limit",
			limits: ResourceLimits{MaxQueueDepth: map[TrafficClass]uint64{
				TrafficClassControl:  8,
				TrafficClassRealtime: 16,
			}},
			samples: []ResourceSample{
				resourceSample(0, 100, 1_000, 1, 1, 4),
				resourceSample(time.Minute, 100, 1_000, 1, 1, 4),
			},
			wantErr: "exactly control, auth and realtime",
		},
		{
			name:   "non-zero baseline time",
			limits: resourceLimits(),
			samples: []ResourceSample{
				resourceSample(time.Second, 100, 1_000, 1, 1, 4),
				resourceSample(time.Minute, 100, 1_000, 1, 1, 4),
			},
			wantErr: "zero-time baseline",
		},
		{
			name:   "unknown sample interval",
			limits: resourceLimits(),
			samples: []ResourceSample{
				resourceSample(0, 100, 1_000, 1, 1, 4),
				resourceSample(0, 100, 1_000, 1, 1, 4),
			},
			wantErr: "not strictly increasing",
		},
		{
			name:   "missing sample queue class",
			limits: resourceLimits(),
			samples: []ResourceSample{
				resourceSample(0, 100, 1_000, 1, 1, 4),
				{
					Elapsed:    time.Minute,
					Goroutines: 100,
					HeapBytes:  1_000,
					QueueDepth: map[TrafficClass]uint64{
						TrafficClassControl:  1,
						TrafficClassRealtime: 4,
					},
				},
			},
			wantErr: "exactly three queue classes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := EvaluateResources(test.limits, test.samples)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("EvaluateResources() error = %v, want %q", err, test.wantErr)
			}
			if result.Passed {
				t.Fatal("invalid resource ledger passed")
			}
		})
	}
}

func resourceLimits() ResourceLimits {
	return ResourceLimits{
		MaxGoroutineGrowth: 10,
		MaxHeapGrowthBytes: 200,
		MaxQueueDepth: map[TrafficClass]uint64{
			TrafficClassControl:  8,
			TrafficClassAuth:     8,
			TrafficClassRealtime: 16,
		},
	}
}

func resourceSample(
	elapsed time.Duration,
	goroutines uint64,
	heapBytes uint64,
	controlDepth uint64,
	authDepth uint64,
	realtimeDepth uint64,
) ResourceSample {
	return ResourceSample{
		Elapsed:    elapsed,
		Goroutines: goroutines,
		HeapBytes:  heapBytes,
		QueueDepth: map[TrafficClass]uint64{
			TrafficClassControl:  controlDepth,
			TrafficClassAuth:     authDepth,
			TrafficClassRealtime: realtimeDepth,
		},
	}
}
