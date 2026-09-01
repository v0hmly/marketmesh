package networksoak

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/platform/testkit/networkchaos"
)

type probeRunResult struct {
	snapshot probe.Snapshot
	err      error
}

type resourceRunResult struct {
	samples []networkchaos.ResourceSample
	err     error
}

type sessionResult struct {
	client    probe.Snapshot
	resources []networkchaos.ResourceSample
}

func runSession(
	ctx context.Context,
	continuousProbe *probe.Runner,
	resourceSampler *networkchaos.ResourceSampler,
	runChaos func(context.Context) error,
) (sessionResult, error) {
	if ctx == nil {
		return sessionResult{}, errors.New("network soak: session context must not be nil")
	}
	if continuousProbe == nil {
		return sessionResult{}, errors.New("network soak: continuous probe is required")
	}
	if resourceSampler == nil {
		return sessionResult{}, errors.New("network soak: resource sampler is required")
	}
	if runChaos == nil {
		return sessionResult{}, errors.New("network soak: chaos runner is required")
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	resourceStop := make(chan struct{})
	probeResults := make(chan probeRunResult, 1)
	resourceResults := make(chan resourceRunResult, 1)
	chaosResults := make(chan error, 1)
	go func() {
		snapshot, err := continuousProbe.Run(sessionCtx)
		probeResults <- probeRunResult{snapshot: snapshot, err: err}
	}()
	go func() {
		samples, err := resourceSampler.Run(sessionCtx, resourceStop)
		resourceResults <- resourceRunResult{samples: samples, err: err}
	}()
	go func() {
		chaosResults <- runChaos(sessionCtx)
	}()

	var (
		probeResult    probeRunResult
		resourceResult resourceRunResult
		chaosErr       error
		prematureErr   error
		probeDone      bool
		resourceDone   bool
	)
	select {
	case probeResult = <-probeResults:
		probeDone = true
		prematureErr = errors.New("network soak: continuous probe stopped before chaos")
	case resourceResult = <-resourceResults:
		resourceDone = true
		prematureErr = errors.New("network soak: resource sampler stopped before chaos")
	case chaosErr = <-chaosResults:
		select {
		case probeResult = <-probeResults:
			probeDone = true
			prematureErr = errors.Join(
				prematureErr,
				errors.New("network soak: continuous probe stopped before chaos"),
			)
		default:
		}
		select {
		case resourceResult = <-resourceResults:
			resourceDone = true
			prematureErr = errors.Join(
				prematureErr,
				errors.New("network soak: resource sampler stopped before chaos"),
			)
		default:
		}
	}

	if prematureErr != nil {
		cancelSession()
		close(resourceStop)
		if !resourceDone {
			resourceResult = <-resourceResults
		}
		if !probeDone {
			probeResult = <-probeResults
		}
		if chaosErr == nil {
			chaosErr = <-chaosResults
		}
	} else {
		close(resourceStop)
		resourceResult = <-resourceResults
		cancelSession()
		probeResult = <-probeResults
	}

	return sessionResult{
		client:    probeResult.snapshot,
		resources: resourceResult.samples,
	}, errors.Join(prematureErr, chaosErr, resourceResult.err, probeResult.err)
}

type successInvoker struct{}

func (successInvoker) Invoke(_ context.Context, request probe.Request) probe.Response {
	route := probe.FakeReadRoute
	if request.Class == probe.TrafficClassMutating {
		route = probe.FakeMutatingRoute
	}
	return probe.Response{
		Outcome:          probe.OutcomeSuccess,
		RouteID:          route,
		DataCenter:       probe.DataCenterA,
		Source:           "fake-internal-a",
		InternalSequence: request.Sequence,
	}
}

type staticResourceSource struct {
	reads chan struct{}
	err   error
}

func (source staticResourceSource) Read(context.Context) (networkchaos.ResourceObservation, error) {
	if source.err != nil {
		return networkchaos.ResourceObservation{}, source.err
	}
	if source.reads != nil {
		select {
		case source.reads <- struct{}{}:
		default:
		}
	}
	return networkchaos.ResourceObservation{
		Goroutines: 10,
		HeapBytes:  1024,
		QueueDepth: map[networkchaos.TrafficClass]uint64{
			networkchaos.TrafficClassControl:  0,
			networkchaos.TrafficClassAuth:     0,
			networkchaos.TrafficClassRealtime: 0,
		},
	}, nil
}

func TestRunSessionKeepsProbeActiveAcrossFaultLifecycleAndJoinsIt(t *testing.T) {
	t.Parallel()

	continuousProbe, err := probe.New(probe.Config{
		RunTimeout:     2 * time.Second,
		StopTimeout:    time.Second,
		RequestTimeout: 100 * time.Millisecond,
		Read: probe.StreamConfig{
			RPS:           100,
			Concurrency:   1,
			QueueCapacity: 8,
		},
		Mutating: probe.StreamConfig{
			RPS:           100,
			Concurrency:   1,
			QueueCapacity: 8,
		},
		RecordCapacity: 128,
		EventCapacity:  512,
	}, successInvoker{}, probe.Dependencies{})
	if err != nil {
		t.Fatalf("probe.New() error = %v", err)
	}
	resourceReads := make(chan struct{}, 4)
	resourceSampler, err := networkchaos.NewResourceSampler(
		networkchaos.ResourceSamplerConfig{
			PollInterval: time.Millisecond,
			ReadTimeout:  100 * time.Millisecond,
			MaxSamples:   100,
		},
		staticResourceSource{reads: resourceReads},
	)
	if err != nil {
		t.Fatalf("networkchaos.NewResourceSampler() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := runSession(ctx, continuousProbe, resourceSampler, func(runCtx context.Context) error {
		for range 2 {
			select {
			case <-resourceReads:
			case <-runCtx.Done():
				return runCtx.Err()
			}
		}
		observer, observerErr := newProbeObserver(
			continuousProbe,
			map[string]markerTarget{
				"partition-a": {
					DataCenter: probe.DataCenterA,
					Zone:       probe.ZoneInternal,
					Component:  probe.ComponentNetwork,
				},
			},
			probe.SteadyRequirement{ReadSuccesses: 1, MutatingSuccesses: 1},
		)
		if observerErr != nil {
			return observerErr
		}
		for _, phase := range []networkchaos.ObservationPhase{
			networkchaos.ObservationPhaseBefore,
			networkchaos.ObservationPhaseActive,
			networkchaos.ObservationPhaseRecovered,
		} {
			if observeErr := observer.Observe(runCtx, networkchaos.Observation{
				FaultIndex: 0,
				FaultCount: 1,
				FaultName:  "partition-a",
				FaultKind:  networkchaos.KindPartition,
				Phase:      phase,
			}); observeErr != nil {
				return observeErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runSession() error = %v", err)
	}
	if !result.client.IsComplete || len(result.client.Records) == 0 {
		t.Fatalf("snapshot = %+v, want complete continuous traffic", result.client)
	}
	resourceGate, err := networkchaos.EvaluateResources(
		networkchaos.ResourceLimits{
			MaxGoroutineGrowth: 0,
			MaxHeapGrowthBytes: 0,
			MaxQueueDepth: map[networkchaos.TrafficClass]uint64{
				networkchaos.TrafficClassControl:  1,
				networkchaos.TrafficClassAuth:     1,
				networkchaos.TrafficClassRealtime: 1,
			},
		},
		result.resources,
	)
	if err != nil {
		t.Fatalf("networkchaos.EvaluateResources() error = %v", err)
	}
	if err := resourceGate.Gate(); err != nil {
		t.Fatalf("resource gate error = %v", err)
	}
	markerPhases := []probe.MarkerPhase{}
	for _, event := range result.client.Events {
		if event.Kind == probe.EventKindMarker {
			markerPhases = append(markerPhases, event.Marker.Phase)
		}
	}
	wantMarkers := []probe.MarkerPhase{
		probe.MarkerPhaseBefore,
		probe.MarkerPhaseStarted,
		probe.MarkerPhaseRecovered,
	}
	if len(markerPhases) != len(wantMarkers) {
		t.Fatalf("marker phases = %v, want %v", markerPhases, wantMarkers)
	}
	for index := range wantMarkers {
		if markerPhases[index] != wantMarkers[index] {
			t.Fatalf("marker phases = %v, want %v", markerPhases, wantMarkers)
		}
	}
}

func TestRunSessionFailsClosedWhenResourceSamplerStopsBeforeChaos(t *testing.T) {
	t.Parallel()

	continuousProbe, err := probe.New(probe.Config{
		RunTimeout:     2 * time.Second,
		StopTimeout:    time.Second,
		RequestTimeout: 100 * time.Millisecond,
		Read: probe.StreamConfig{
			RPS:           100,
			Concurrency:   1,
			QueueCapacity: 8,
		},
		RecordCapacity: 64,
		EventCapacity:  64,
	}, successInvoker{}, probe.Dependencies{})
	if err != nil {
		t.Fatalf("probe.New() error = %v", err)
	}
	resourceSampler, err := networkchaos.NewResourceSampler(
		networkchaos.ResourceSamplerConfig{
			PollInterval: time.Millisecond,
			ReadTimeout:  100 * time.Millisecond,
			MaxSamples:   10,
		},
		staticResourceSource{err: errors.New("metrics unavailable")},
	)
	if err != nil {
		t.Fatalf("networkchaos.NewResourceSampler() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := runSession(ctx, continuousProbe, resourceSampler, func(runCtx context.Context) error {
		<-runCtx.Done()
		return runCtx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "resource sampler stopped before chaos") ||
		!strings.Contains(err.Error(), "metrics unavailable") {
		t.Fatalf("runSession() error = %v, want premature resource failure", err)
	}
	if len(result.resources) != 0 {
		t.Fatalf("resource samples = %v, want no accepted baseline", result.resources)
	}
	if !result.client.IsComplete {
		t.Fatalf("probe snapshot = %+v, want bounded complete shutdown", result.client)
	}
}
