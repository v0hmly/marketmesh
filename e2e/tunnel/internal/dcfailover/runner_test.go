package dcfailover

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	_ Topology  = (*fakeAdapters)(nil)
	_ Drainer   = (*fakeAdapters)(nil)
	_ FrontDoor = (*fakeAdapters)(nil)
	_ Readiness = (*fakeAdapters)(nil)
	_ Probe     = (*fakeAdapters)(nil)
)

func TestRunExecutesSymmetricManagedAndSuddenOutages(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	runner := newTestRunner(t, adapters)
	if err := runner.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantStops := []string{
		"topology.stop:dc-a:managed",
		"topology.stop:dc-a:sudden",
		"topology.stop:dc-b:managed",
		"topology.stop:dc-b:sudden",
	}
	if got := eventsWithPrefix(adapters.events, "topology.stop:"); !slices.Equal(got, wantStops) {
		t.Fatalf("stop events = %v, want %v", got, wantStops)
	}

	wantDrains := []string{"drainer.drain:dc-a", "drainer.drain:dc-b"}
	if got := eventsWithPrefix(adapters.events, "drainer.drain:"); !slices.Equal(got, wantDrains) {
		t.Fatalf("drain events = %v, want %v", got, wantDrains)
	}

	if got := countEvent(adapters.events, "frontdoor.check"); got != 8 {
		t.Fatalf("front door check count = %d, want 8", got)
	}

	if got := countEvent(adapters.events, "readiness.baseline"); got != 5 {
		t.Fatalf("baseline count = %d, want 5", got)
	}
	assertFinalizationOrder(t, adapters.events)
	assertExactTargets(t, adapters.targets)
}

func TestRunRejectsUnsafeSnapshotBeforeMutationOrCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{
			name: "not disposable",
			mutate: func(snapshot *Snapshot) {
				snapshot.Disposable = false
			},
			want: "not disposable local e2e",
		},
		{
			name: "foreign owner",
			mutate: func(snapshot *Snapshot) {
				snapshot.Clusters[0].OwnerRunID = "other-run"
			},
			want: "not owned by this run",
		},
		{
			name: "glob context",
			mutate: func(snapshot *Snapshot) {
				snapshot.Clusters[0].KubeContext = "dc-*"
			},
			want: "must not contain glob characters",
		},
		{
			name: "duplicate container",
			mutate: func(snapshot *Snapshot) {
				snapshot.Clusters[1].ContainerNames[0] = snapshot.Clusters[0].ContainerNames[0]
			},
			want: "container name",
		},
		{
			name: "missing network",
			mutate: func(snapshot *Snapshot) {
				snapshot.Clusters[0].NetworkNames = []string{}
			},
			want: "exact container and network names are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapters := newFakeAdapters()
			tt.mutate(&adapters.snapshot)
			runner := newTestRunner(t, adapters)
			err := runner.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, tt.want)
			}
			if !slices.Equal(adapters.events, []string{"topology.preflight"}) {
				t.Fatalf("events = %v, want preflight only", adapters.events)
			}
		})
	}
}

func TestRunWaitsForReadinessBeforeFailback(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.failures["readiness.restored:dc-a"] = errors.New("pki is not ready")
	runner := newTestRunner(t, adapters)
	err := runner.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "pki is not ready") {
		t.Fatalf("Run() error = %v, want readiness error", err)
	}
	if got := countEvent(adapters.events, "frontdoor.check"); got != 1 {
		t.Fatalf("failback started before readiness: %v", adapters.events)
	}
	assertFinalizationOrder(t, adapters.events)
}

func TestRunPreservesPrimaryAndFinalizationErrors(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.failures["topology.stop:dc-a:managed"] = errors.New("stop failed")
	adapters.failures["topology.inspect"] = errors.New("inspect failed")
	adapters.failures["topology.cleanup"] = errors.New("cleanup failed")
	runner := newTestRunner(t, adapters)
	err := runner.Run(t.Context())
	if err == nil {
		t.Fatal("Run() error = nil, want joined error")
	}
	for _, message := range []string{"stop failed", "inspect failed", "cleanup failed"} {
		if !strings.Contains(err.Error(), message) {
			t.Fatalf("Run() error = %v, want containing %q", err, message)
		}
	}
	assertFinalizationOrder(t, adapters.events)
}

func TestRunUsesBoundedContextsAndIsSingleUse(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.requireDeadline = true
	runner := newTestRunner(t, adapters)
	if err := runner.Run(t.Context()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if adapters.checkWithoutDeadline {
		t.Fatal("front door Check() received a context without a deadline")
	}
	err := runner.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "single-use") {
		t.Fatalf("second Run() error = %v, want single-use rejection", err)
	}
}

func TestRunFinalizesValidatedResourcesWhenProbeStartFails(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.failures["probe.start"] = errors.New("probe unavailable")
	runner := newTestRunner(t, adapters)
	err := runner.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "probe unavailable") {
		t.Fatalf("Run() error = %v, want probe start error", err)
	}

	wantFinalization := []string{"topology.inspect", "topology.cleanup"}
	if got := adapters.events[len(adapters.events)-len(wantFinalization):]; !slices.Equal(got, wantFinalization) {
		t.Fatalf("finalization events = %v, want %v", got, wantFinalization)
	}
	if slices.Contains(adapters.events, "probe.stop") || slices.Contains(adapters.events, "probe.verify") {
		t.Fatalf("probe that did not start was finalized: %v", adapters.events)
	}
}

func TestRunRejectsNilContextWithoutPreflight(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	runner := newTestRunner(t, adapters)
	err := runner.Run(nil)
	if err == nil || !strings.Contains(err.Error(), "context must not be nil") {
		t.Fatalf("Run() error = %v, want nil context rejection", err)
	}
	if len(adapters.events) != 0 {
		t.Fatalf("events = %v, want no adapter calls", adapters.events)
	}
}

func TestNewRejectsUnboundedConfiguration(t *testing.T) {
	t.Parallel()

	valid := testConfig()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "zero phase timeout",
			mutate: func(config *Config) {
				config.PhaseTimeout = 0
			},
		},
		{
			name: "large finalize timeout",
			mutate: func(config *Config) {
				config.FinalizeTimeout = 31 * time.Minute
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			tt.mutate(&config)
			adapters := newFakeAdapters()
			if _, err := New(config, testDependencies(adapters)); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func TestNewRequiresEveryAdapter(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	dependencies := testDependencies(adapters)
	dependencies.Topology = nil
	if _, err := New(testConfig(), dependencies); err == nil {
		t.Fatal("New() error = nil, want missing adapter rejection")
	}
}

func TestRunDefensivelyCopiesExactTargets(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.mutateStopTarget = true
	runner := newTestRunner(t, adapters)
	if err := runner.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, target := range adapters.restoreTargets {
		for _, cluster := range target.Clusters {
			if cluster.Name == "mutated" || slices.Contains(cluster.ContainerNames, "mutated") {
				t.Fatalf("restore target was mutated through adapter alias: %+v", target)
			}
		}
	}
}

func newTestRunner(t *testing.T, adapters *fakeAdapters) *Runner {
	t.Helper()

	runner, err := New(testConfig(), testDependencies(adapters))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return runner
}

func testConfig() Config {
	return Config{
		RunID:           "mm35-run",
		PhaseTimeout:    time.Minute,
		FinalizeTimeout: time.Minute,
	}
}

func testDependencies(adapters *fakeAdapters) Dependencies {
	return Dependencies{
		Topology:  adapters,
		Drainer:   adapters,
		FrontDoor: adapters,
		Readiness: adapters,
		Probe:     adapters,
	}
}

func safeSnapshot() Snapshot {
	clusters := make([]Cluster, 0, 4)
	for _, dc := range []DC{DCA, DCB} {
		for _, zone := range []Zone{ZoneDMZ, ZoneInternal} {
			name := fmt.Sprintf("mm35-run-%s-%s", dc, zone)
			clusters = append(clusters, Cluster{
				DC:             dc,
				Zone:           zone,
				Name:           name,
				Kubeconfig:     "/tmp/" + name + ".yaml",
				KubeContext:    name,
				OwnerRunID:     "mm35-run",
				ContainerNames: []string{name + "-control-plane"},
				NetworkNames:   []string{name + "-network"},
			})
		}
	}

	return Snapshot{
		RunID:       "mm35-run",
		Environment: EnvironmentLocalE2E,
		Disposable:  true,
		Clusters:    clusters,
	}
}

type fakeAdapters struct {
	snapshot             Snapshot
	events               []string
	failures             map[string]error
	targets              []DCTarget
	restoreTargets       []DCTarget
	requireDeadline      bool
	checkWithoutDeadline bool
	mutateStopTarget     bool
}

func newFakeAdapters() *fakeAdapters {
	return &fakeAdapters{
		snapshot:       safeSnapshot(),
		events:         []string{},
		failures:       map[string]error{},
		targets:        []DCTarget{},
		restoreTargets: []DCTarget{},
	}
}

func (adapters *fakeAdapters) Preflight(ctx context.Context, _ string) (Snapshot, error) {
	if err := adapters.record(ctx, "topology.preflight"); err != nil {
		return Snapshot{}, err
	}

	return cloneSnapshot(adapters.snapshot), nil
}

func (adapters *fakeAdapters) StopDC(
	ctx context.Context,
	target DCTarget,
	kind OutageKind,
) error {
	adapters.targets = append(adapters.targets, cloneTarget(target))
	event := fmt.Sprintf("topology.stop:%s:%s", target.DC, kind)
	if adapters.mutateStopTarget {
		target.Clusters[0].Name = "mutated"
		target.Clusters[0].ContainerNames[0] = "mutated"
	}

	return adapters.record(ctx, event)
}

func (adapters *fakeAdapters) RestoreDC(ctx context.Context, target DCTarget) error {
	adapters.restoreTargets = append(adapters.restoreTargets, cloneTarget(target))

	return adapters.record(ctx, "topology.restore:"+string(target.DC))
}

func (adapters *fakeAdapters) Inspect(ctx context.Context, _ Snapshot) error {
	return adapters.record(ctx, "topology.inspect")
}

func (adapters *fakeAdapters) Cleanup(ctx context.Context, _ Snapshot) error {
	return adapters.record(ctx, "topology.cleanup")
}

func (adapters *fakeAdapters) DrainDC(ctx context.Context, dc DC) error {
	return adapters.record(ctx, "drainer.drain:"+string(dc))
}

func (adapters *fakeAdapters) Check(ctx context.Context) {
	if _, found := ctx.Deadline(); !found {
		adapters.checkWithoutDeadline = true
	}
	_ = adapters.record(ctx, "frontdoor.check")
}

func (adapters *fakeAdapters) WaitExcluded(ctx context.Context, dc DC) error {
	return adapters.record(ctx, "readiness.excluded:"+string(dc))
}

func (adapters *fakeAdapters) WaitEligible(ctx context.Context, dc DC) error {
	return adapters.record(ctx, "readiness.eligible:"+string(dc))
}

func (adapters *fakeAdapters) WaitBaseline(ctx context.Context) error {
	return adapters.record(ctx, "readiness.baseline")
}

func (adapters *fakeAdapters) WaitSurvivor(ctx context.Context, dc DC) error {
	return adapters.record(ctx, "readiness.survivor:"+string(dc))
}

func (adapters *fakeAdapters) WaitRestored(ctx context.Context, dc DC) error {
	return adapters.record(ctx, "readiness.restored:"+string(dc))
}

func (adapters *fakeAdapters) Start(ctx context.Context) error {
	return adapters.record(ctx, "probe.start")
}

func (adapters *fakeAdapters) Mark(ctx context.Context, marker Marker) error {
	return adapters.record(ctx, fmt.Sprintf(
		"probe.mark:%s:%s:%s",
		marker.DC,
		marker.OutageKind,
		marker.Phase,
	))
}

func (adapters *fakeAdapters) Stop(ctx context.Context) error {
	return adapters.record(ctx, "probe.stop")
}

func (adapters *fakeAdapters) Verify(ctx context.Context) error {
	return adapters.record(ctx, "probe.verify")
}

func (adapters *fakeAdapters) record(ctx context.Context, event string) error {
	adapters.events = append(adapters.events, event)
	if adapters.requireDeadline {
		if _, found := ctx.Deadline(); !found {
			return errors.New("context has no deadline")
		}
	}

	return adapters.failures[event]
}

func eventsWithPrefix(events []string, prefix string) []string {
	result := []string{}
	for _, event := range events {
		if strings.HasPrefix(event, prefix) {
			result = append(result, event)
		}
	}

	return result
}

func countEvent(events []string, expected string) int {
	count := 0
	for _, event := range events {
		if event == expected {
			count++
		}
	}

	return count
}

func assertFinalizationOrder(t *testing.T, events []string) {
	t.Helper()

	want := []string{"probe.stop", "probe.verify", "topology.inspect", "topology.cleanup"}
	if len(events) < len(want) {
		t.Fatalf("events = %v, want finalization %v", events, want)
	}
	if got := events[len(events)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("finalization events = %v, want %v", got, want)
	}
}

func assertExactTargets(t *testing.T, targets []DCTarget) {
	t.Helper()

	if len(targets) != 4 {
		t.Fatalf("target count = %d, want 4", len(targets))
	}
	for _, target := range targets {
		if len(target.Clusters) != 2 {
			t.Fatalf("target %+v does not contain exactly two clusters", target)
		}
		seenZones := map[Zone]bool{}
		for _, cluster := range target.Clusters {
			if cluster.DC != target.DC {
				t.Fatalf("target dc = %s, cluster dc = %s", target.DC, cluster.DC)
			}
			seenZones[cluster.Zone] = true
		}
		if !seenZones[ZoneDMZ] || !seenZones[ZoneInternal] {
			t.Fatalf("target %+v does not contain dmz and internal", target)
		}
	}
}
