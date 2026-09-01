package rolling

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"google.golang.org/grpc"
)

func TestLedgerArchivePreservesReplacementWithoutResolverGap(t *testing.T) {
	t.Parallel()

	runtime := newArchiveRuntimeStub(replacementPodStates())
	runtime.entries["fake-a-old-1"] = []*e2ev1.LedgerEntry{
		ledgerEntry(1, e2ev1.Operation_OPERATION_READ, 1),
	}
	runtime.entries["fake-a-new-1"] = []*e2ev1.LedgerEntry{
		ledgerEntry(1, e2ev1.Operation_OPERATION_MUTATE, 2),
	}
	archive := newTestLedgerArchive(t, runtime, 100)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- archive.Run(ctx) }()
	waitArchiveReady(t, archive)

	stopReaders := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for {
				select {
				case <-stopReaders:
					return
				default:
					_, _ = archive.Resolve("fake-a-old-1")
					_, _ = archive.Resolve("fake-a-new-1")
				}
			}
		})
	}
	waitForArchiveCalls(t, runtime, 8)
	close(stopReaders)
	readers.Wait()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("LedgerArchive.Run() error = %v", err)
	}

	for _, source := range []string{"fake-a-old-1", "fake-a-new-1"} {
		dataCenter, found := archive.Resolve(source)
		if !found || dataCenter != probe.DataCenterA {
			t.Fatalf("Resolve(%s) = %q, %v", source, dataCenter, found)
		}
	}
	snapshot := archive.Snapshot()
	if !snapshot.IsComplete || len(snapshot.IncompleteReasons) != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(snapshot.Records) != 2 ||
		snapshot.Records[0].Source != "fake-a-new-1" ||
		snapshot.Records[1].Source != "fake-a-old-1" {
		t.Fatalf("records = %#v", snapshot.Records)
	}
}

func TestLedgerArchiveFailsClosedForMissedPreStopSnapshot(t *testing.T) {
	t.Parallel()

	states := replacementPodStates()
	states[1]["dc-a"] = []archivePod{
		readyArchivePod("fake-a-old-1", "uid-a-old-1"),
		readyArchivePod("fake-a-new-1", "uid-a-new-1"),
	}
	states = states[:2]
	runtime := newArchiveRuntimeStub(states)
	runtime.entries["fake-a-old-1"] = []*e2ev1.LedgerEntry{
		ledgerEntry(1, e2ev1.Operation_OPERATION_READ, 1),
	}
	archive := newTestLedgerArchive(t, runtime, 100)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- archive.Run(ctx) }()
	waitArchiveReady(t, archive)
	waitForArchiveCalls(t, runtime, 4)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("LedgerArchive.Run() error = nil")
	}
	snapshot := archive.Snapshot()
	if snapshot.IsComplete || !slices.Contains(
		snapshot.IncompleteReasons,
		"archive_final_snapshot_missed",
	) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, found := archive.Resolve("fake-a-old-1"); found {
		t.Fatal("stale resolver remained healthy after missed final snapshot")
	}
}

func TestLedgerArchiveRejectsDuplicateInstanceAcrossDataCenters(t *testing.T) {
	t.Parallel()

	duplicate := "fake-shared-1"
	states := []map[string][]archivePod{{
		"dc-a": {
			readyArchivePod(duplicate, "uid-a-shared"),
			readyArchivePod("fake-a-2", "uid-a-2"),
		},
		"dc-b": {
			readyArchivePod(duplicate, "uid-b-shared"),
			readyArchivePod("fake-b-2", "uid-b-2"),
		},
	}}
	archive := newTestLedgerArchive(t, newArchiveRuntimeStub(states), 100)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- archive.Run(ctx) }()
	readyCtx, readyCancel := context.WithTimeout(t.Context(), time.Second)
	defer readyCancel()
	if err := archive.WaitReady(readyCtx); err == nil {
		t.Fatal("WaitReady() error = nil")
	}
	if err := <-done; err == nil {
		t.Fatal("LedgerArchive.Run() error = nil")
	}
	if _, found := archive.Resolve(duplicate); found {
		t.Fatal("duplicate source remained resolvable")
	}
}

func TestLedgerArchiveRejectsStaleResolverAfterPodNameReuse(t *testing.T) {
	t.Parallel()

	states := replacementPodStates()[:2]
	states[1]["dc-a"][0].UID = "uid-a-reused"
	runtime := newArchiveRuntimeStub(states)
	archive := newTestLedgerArchive(t, runtime, 100)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- archive.Run(ctx) }()
	waitArchiveReady(t, archive)
	waitForArchiveCalls(t, runtime, 4)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("LedgerArchive.Run() error = nil")
	}
	if _, found := archive.Resolve("fake-a-old-1"); found {
		t.Fatal("reused instance remained resolvable")
	}
	if snapshot := archive.Snapshot(); !slices.Contains(
		snapshot.IncompleteReasons,
		"archive_instance_reused",
	) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestLedgerArchiveBoundsHungDiscovery(t *testing.T) {
	t.Parallel()

	runtime := newArchiveRuntimeStub(replacementPodStates()[:1])
	runtime.hung["fake-a-old-1"] = true
	archive := newTestLedgerArchive(t, runtime, 100)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	startedAt := time.Now()
	go func() { done <- archive.Run(ctx) }()
	readyCtx, readyCancel := context.WithTimeout(t.Context(), time.Second)
	defer readyCancel()
	if err := archive.WaitReady(readyCtx); err == nil {
		t.Fatal("WaitReady() error = nil")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("hung discovery elapsed = %s", elapsed)
	}
	if err := <-done; err == nil {
		t.Fatal("LedgerArchive.Run() error = nil")
	}
}

func TestLedgerArchiveRejectsPartialInitialSnapshot(t *testing.T) {
	t.Parallel()

	runtime := newArchiveRuntimeStub(replacementPodStates()[:1])
	runtime.entries["fake-a-old-1"] = []*e2ev1.LedgerEntry{
		ledgerEntry(1, e2ev1.Operation_OPERATION_READ, 1),
	}
	archive := newTestLedgerArchive(t, runtime, 1)
	done := make(chan error, 1)
	go func() { done <- archive.Run(t.Context()) }()
	readyCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := archive.WaitReady(readyCtx); err == nil {
		t.Fatal("WaitReady() error = nil")
	}
	if err := <-done; err == nil {
		t.Fatal("LedgerArchive.Run() error = nil")
	}
	if snapshot := archive.Snapshot(); snapshot.IsComplete || !slices.Contains(
		snapshot.IncompleteReasons,
		"archive_collect_failed",
	) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func newTestLedgerArchive(
	t *testing.T,
	runtime archiveRuntime,
	limit uint32,
) *LedgerArchive {
	t.Helper()
	archive, err := newLedgerArchive(LedgerArchiveConfig{
		RunID: "mm34-run", Clusters: testInternalClusters(),
		PollInterval: 10 * time.Millisecond, CallTimeout: 50 * time.Millisecond,
		StopTimeout: 6 * time.Second, LedgerLimit: limit,
	}, runtime)
	if err != nil {
		t.Fatalf("newLedgerArchive() error = %v", err)
	}
	return archive
}

func waitArchiveReady(t *testing.T, archive *LedgerArchive) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := archive.WaitReady(ctx); err != nil {
		t.Fatalf("LedgerArchive.WaitReady() error = %v", err)
	}
}

func waitForArchiveCalls(t *testing.T, runtime *archiveRuntimeStub, count uint32) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for runtime.calls.Load() < count {
		select {
		case <-deadline.C:
			t.Fatalf("ListPods calls = %d, want at least %d", runtime.calls.Load(), count)
		case <-ticker.C:
		}
	}
}

func testInternalClusters() []Cluster {
	return []Cluster{
		{
			LogicalName: "dc-a-internal", ResourceName: "mm34-dc-a-internal",
			TopologyInstance: "mm34", ControlPlaneAddress: "127.0.0.2",
			DC: "dc-a", Zone: "internal", Kubeconfig: "/tmp/mm34-a",
			Context: "kind-mm34-dc-a-internal",
		},
		{
			LogicalName: "dc-b-internal", ResourceName: "mm34-dc-b-internal",
			TopologyInstance: "mm34", ControlPlaneAddress: "127.0.0.3",
			DC: "dc-b", Zone: "internal", Kubeconfig: "/tmp/mm34-b",
			Context: "kind-mm34-dc-b-internal",
		},
	}
}

func replacementPodStates() []map[string][]archivePod {
	stableB := []archivePod{
		readyArchivePod("fake-b-old-1", "uid-b-old-1"),
		readyArchivePod("fake-b-old-2", "uid-b-old-2"),
	}
	return []map[string][]archivePod{
		{
			"dc-a": {
				readyArchivePod("fake-a-old-1", "uid-a-old-1"),
				readyArchivePod("fake-a-old-2", "uid-a-old-2"),
			},
			"dc-b": stableB,
		},
		{
			"dc-a": {
				readyArchivePod("fake-a-old-1", "uid-a-old-1"),
				readyArchivePod("fake-a-old-2", "uid-a-old-2"),
				{Name: "fake-a-new-1", UID: "uid-a-new-1", Running: true},
			},
			"dc-b": stableB,
		},
		{
			"dc-a": {
				readyArchivePod("fake-a-old-1", "uid-a-old-1"),
				{
					Name: "fake-a-old-2", UID: "uid-a-old-2", Running: true,
					Terminating: true,
				},
				readyArchivePod("fake-a-new-1", "uid-a-new-1"),
			},
			"dc-b": stableB,
		},
		{
			"dc-a": {
				readyArchivePod("fake-a-old-1", "uid-a-old-1"),
				readyArchivePod("fake-a-new-1", "uid-a-new-1"),
			},
			"dc-b": stableB,
		},
	}
}

func readyArchivePod(name string, uid string) archivePod {
	return archivePod{Name: name, UID: uid, Running: true, Ready: true}
}

func ledgerEntry(
	sequence uint64,
	operation e2ev1.Operation,
	requestByte byte,
) *e2ev1.LedgerEntry {
	entry := &e2ev1.LedgerEntry{
		Sequence: sequence, Operation: operation,
		RequestId: slices.Repeat([]byte{requestByte}, 16), Attempts: 1,
	}
	if operation == e2ev1.Operation_OPERATION_MUTATE {
		digest := sha256.Sum256(entry.RequestId)
		entry.IdempotencyKeySha256 = digest[:]
	}
	return entry
}

type archiveRuntimeStub struct {
	mu      sync.RWMutex
	states  []map[string][]archivePod
	entries map[string][]*e2ev1.LedgerEntry
	hung    map[string]bool
	calls   atomic.Uint32
}

func newArchiveRuntimeStub(states []map[string][]archivePod) *archiveRuntimeStub {
	return &archiveRuntimeStub{
		states: states, entries: make(map[string][]*e2ev1.LedgerEntry),
		hung: make(map[string]bool),
	}
}

func (runtime *archiveRuntimeStub) ListPods(
	_ context.Context,
	cluster Cluster,
) ([]archivePod, error) {
	call := runtime.calls.Add(1)
	stateIndex := min(int((call-1)/2), len(runtime.states)-1)
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return slices.Clone(runtime.states[stateIndex][cluster.DC]), nil
}

func (runtime *archiveRuntimeStub) Open(
	_ context.Context,
	_ Cluster,
	pod archivePod,
) (archiveConnection, error) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return &archiveConnectionStub{
		source: pod.Name, entries: slices.Clone(runtime.entries[pod.Name]),
		hung: runtime.hung[pod.Name],
	}, nil
}

type archiveConnectionStub struct {
	source  string
	entries []*e2ev1.LedgerEntry
	hung    bool
	closed  atomic.Bool
}

func (connection *archiveConnectionStub) Ledger(
	ctx context.Context,
	request *e2ev1.LedgerRequest,
	_ ...grpc.CallOption,
) (*e2ev1.LedgerResponse, error) {
	if connection.hung {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if connection.closed.Load() {
		return nil, errors.New("closed")
	}
	limit := min(int(request.GetLimit()), len(connection.entries))
	return &e2ev1.LedgerResponse{
		InstanceId: connection.source,
		Entries:    slices.Clone(connection.entries[len(connection.entries)-limit:]),
	}, nil
}

func (connection *archiveConnectionStub) Close() error {
	connection.closed.Store(true)
	return nil
}
