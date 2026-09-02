package rolling

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

const maximumArchiveInstances = 64

// LedgerArchiveConfig bounds dynamic direct-ledger discovery during rolling
// replacement of the in-memory fake internal service.
type LedgerArchiveConfig struct {
	RunID        string
	Clusters     []Cluster
	PollInterval time.Duration
	CallTimeout  time.Duration
	StopTimeout  time.Duration
	LedgerLimit  uint32
	RecordLimit  int
}

type archivePod struct {
	Name        string
	UID         string
	OwnerName   string
	OwnerUID    string
	Running     bool
	Ready       bool
	Terminating bool
}

type archiveConnection interface {
	probe.FakeLedgerClient
	Close() error
}

type archiveRuntime interface {
	ListPods(ctx context.Context, cluster Cluster) ([]archivePod, error)
	Open(ctx context.Context, cluster Cluster, pod archivePod) (archiveConnection, error)
}

type trackedArchiveInstance struct {
	pod           archivePod
	dataCenter    probe.DataCenter
	connection    archiveConnection
	finalObserved bool
}

// LedgerArchive preserves immutable ledger entries from every exact
// fake-internal Pod generation and concurrently resolves newly discovered
// instance IDs for the front-door probe.
type LedgerArchive struct {
	config  LedgerArchiveConfig
	runtime archiveRuntime
	used    atomic.Bool

	ready     chan struct{}
	readyOnce sync.Once
	readyMu   sync.RWMutex
	readyErr  error

	mu         sync.RWMutex
	healthy    bool
	instances  map[string]probe.DataCenter
	seenUIDs   map[string]string
	tracked    map[string]*trackedArchiveInstance
	records    map[string]probe.InternalRecord
	incomplete map[string]struct{}
}

// NewLedgerArchive validates explicit clusters and creates the production
// kubectl/port-forward runtime without reading ambient kubeconfig.
func NewLedgerArchive(config LedgerArchiveConfig) (*LedgerArchive, error) {
	runtime, clusters, err := newLedgerArchiveRuntime(config)
	if err != nil {
		return nil, err
	}
	config.Clusters = clusters

	return newLedgerArchive(config, runtime)
}

func newLedgerArchive(
	config LedgerArchiveConfig,
	runtime archiveRuntime,
) (*LedgerArchive, error) {
	if err := validateLedgerArchiveConfig(config); err != nil {
		return nil, err
	}
	if isNilDependency(runtime) {
		return nil, errors.New("rolling: ledger archive runtime is required")
	}

	return &LedgerArchive{
		config:     config,
		runtime:    runtime,
		ready:      make(chan struct{}),
		healthy:    true,
		instances:  make(map[string]probe.DataCenter, maximumArchiveInstances),
		seenUIDs:   make(map[string]string, maximumArchiveInstances),
		tracked:    make(map[string]*trackedArchiveInstance, maximumArchiveInstances),
		records:    make(map[string]probe.InternalRecord),
		incomplete: make(map[string]struct{}),
	}, nil
}

// Run owns polling and direct connections until ctx cancellation. The first
// refresh must prove two ready replicas in each DC before WaitReady succeeds.
func (archive *LedgerArchive) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("rolling: ledger archive context must not be nil")
	}
	if !archive.used.CompareAndSwap(false, true) {
		return errors.New("rolling: ledger archive has already been used")
	}

	initialCtx, cancelInitial := context.WithTimeout(ctx, archive.config.CallTimeout)
	initialErr := archive.refresh(initialCtx, true)
	cancelInitial()
	if initialErr == nil && !archive.isHealthy() {
		initialErr = errors.New("rolling: initial ledger archive is incomplete")
	}
	archive.signalReady(initialErr)
	if initialErr != nil {
		archive.markIncomplete("archive_initial_discovery_failed")
		return errors.Join(initialErr, archive.closeAll())
	}

	ticker := time.NewTicker(archive.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			refreshTimeout := min(
				archive.config.CallTimeout,
				archive.config.StopTimeout-portForwardShutdownTimeout,
			)
			finalCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				refreshTimeout,
			)
			if err := archive.refresh(finalCtx, false); err != nil {
				archive.markIncomplete("archive_final_refresh_failed")
			}
			cancel()
			closeErr := archive.closeAll()
			snapshot := archive.Snapshot()
			if !snapshot.IsComplete {
				return errors.Join(closeErr, fmt.Errorf(
					"rolling: ledger archive is incomplete: %s",
					strings.Join(snapshot.IncompleteReasons, ","),
				))
			}
			return closeErr
		case <-ticker.C:
			pollCtx, cancel := context.WithTimeout(ctx, archive.config.CallTimeout)
			if err := archive.refresh(pollCtx, false); err != nil {
				archive.markIncomplete("archive_refresh_failed")
			}
			cancel()
		}
	}
}

// WaitReady blocks only until the initial exact discovery succeeds or fails.
func (archive *LedgerArchive) WaitReady(ctx context.Context) error {
	if ctx == nil {
		return errors.New("rolling: ledger archive readiness context must not be nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-archive.ready:
		archive.readyMu.RLock()
		defer archive.readyMu.RUnlock()
		return archive.readyErr
	}
}

// Resolve implements probe.InstanceResolver without I/O. Once integrity is
// uncertain it fails closed for all responses instead of using stale mapping.
func (archive *LedgerArchive) Resolve(source string) (probe.DataCenter, bool) {
	archive.mu.RLock()
	defer archive.mu.RUnlock()
	if !archive.healthy {
		return probe.DataCenterUnknown, false
	}
	dataCenter, found := archive.instances[source]
	return dataCenter, found
}

// Snapshot returns a deterministic defensive copy of all archived entries.
func (archive *LedgerArchive) Snapshot() probe.InternalSnapshot {
	archive.mu.RLock()
	defer archive.mu.RUnlock()

	records := make([]probe.InternalRecord, 0, len(archive.records))
	for _, record := range archive.records {
		records = append(records, record)
	}
	slices.SortFunc(records, func(left, right probe.InternalRecord) int {
		if left.Source != right.Source {
			return compareString(left.Source, right.Source)
		}
		if left.Sequence < right.Sequence {
			return -1
		}
		if left.Sequence > right.Sequence {
			return 1
		}
		return compareString(left.RequestID, right.RequestID)
	})
	reasons := make([]string, 0, len(archive.incomplete))
	for reason := range archive.incomplete {
		reasons = append(reasons, reason)
	}
	slices.Sort(reasons)

	return probe.InternalSnapshot{
		Records:           records,
		IsComplete:        archive.healthy && len(reasons) == 0 && len(records) > 0,
		IncompleteReasons: reasons,
	}
}

func (archive *LedgerArchive) refresh(ctx context.Context, initial bool) error {
	if ctx == nil {
		return errors.New("rolling: ledger archive refresh context must not be nil")
	}
	current := make(map[string]archivePod, maximumArchiveInstances)
	for _, cluster := range archive.config.Clusters {
		pods, err := archive.runtime.ListPods(ctx, cluster)
		if err != nil {
			return fmt.Errorf("rolling: listing exact ledger Pods: %w", err)
		}
		readyCount := 0
		for _, pod := range pods {
			if pod.Ready && !pod.Terminating {
				readyCount++
			}
			key := cluster.DC + "/" + pod.Name
			if _, exists := current[key]; exists {
				return errors.New("rolling: duplicate ledger Pod in runtime inventory")
			}
			current[key] = pod
			if err := archive.ensureInstance(ctx, cluster, pod, initial); err != nil {
				if initial || pod.Ready {
					return err
				}
			}
		}
		if readyCount < 2 {
			return errors.New("rolling: fake internal ready capacity dropped below two")
		}
		if initial && (len(pods) != 2 || readyCount != 2) {
			return errors.New("rolling: initial fake internal replica set is not exact")
		}
		if len(pods) < 2 || len(pods) > 3 {
			return errors.New("rolling: fake internal replica count is outside rollout bounds")
		}
	}

	archive.mu.Lock()
	tracked := make([]*trackedArchiveInstance, 0, len(archive.tracked))
	for key, instance := range archive.tracked {
		pod, present := current[key]
		if !present {
			if !instance.finalObserved {
				archive.failLocked("archive_final_snapshot_missed")
			}
			delete(archive.tracked, key)
			tracked = append(tracked, instance)
			continue
		}
		instance.pod = pod
	}
	active := make([]*trackedArchiveInstance, 0, len(archive.tracked))
	for _, instance := range archive.tracked {
		active = append(active, instance)
	}
	archive.mu.Unlock()

	for _, instance := range active {
		collected, err := archive.collect(ctx, instance)
		if err != nil {
			archive.markIncomplete("archive_collect_failed")
			continue
		}
		if !instance.pod.Ready || instance.pod.Terminating {
			archive.mu.Lock()
			instance.finalObserved = true
			archive.mu.Unlock()
		}
		if !collected {
			archive.markIncomplete("archive_partial_snapshot")
		}
	}
	for _, instance := range tracked {
		if err := instance.connection.Close(); err != nil {
			archive.markIncomplete("archive_connection_close_failed")
		}
	}

	return nil
}

func (archive *LedgerArchive) ensureInstance(
	ctx context.Context,
	cluster Cluster,
	pod archivePod,
	initial bool,
) error {
	key := cluster.DC + "/" + pod.Name
	archive.mu.RLock()
	tracked, found := archive.tracked[key]
	archive.mu.RUnlock()
	if found {
		if tracked.pod.UID != pod.UID {
			archive.markIncomplete("archive_instance_reused")
			return errors.New("rolling: ledger instance name was reused")
		}
		return nil
	}
	if !pod.Running {
		return nil
	}

	dataCenter, err := probeDataCenter(cluster.DC)
	if err != nil {
		return err
	}
	connection, err := archive.runtime.Open(ctx, cluster, pod)
	if err != nil {
		return errors.New("rolling: opening direct ledger connection")
	}
	collector, err := probe.NewLedgerCollector([]probe.LedgerSource{{
		DataCenter: dataCenter,
		Client:     connection,
	}}, archive.config.LedgerLimit)
	if err != nil {
		_ = connection.Close()
		return err
	}
	directory, err := collector.Discover(ctx)
	if err != nil {
		_ = connection.Close()
		return errors.New("rolling: discovering direct ledger instance")
	}
	resolved, found := directory.Resolve(pod.Name)
	if !found || resolved != dataCenter {
		_ = connection.Close()
		return errors.New("rolling: direct ledger identity mismatch")
	}

	archive.mu.Lock()
	defer archive.mu.Unlock()
	if len(archive.instances) >= maximumArchiveInstances {
		_ = connection.Close()
		archive.failLocked("archive_instance_capacity")
		return errors.New("rolling: ledger instance capacity exceeded")
	}
	if existingDC, exists := archive.instances[pod.Name]; exists && existingDC != dataCenter {
		_ = connection.Close()
		archive.failLocked("archive_instance_duplicate")
		return errors.New("rolling: duplicate ledger instance identity")
	}
	if existingUID, exists := archive.seenUIDs[pod.Name]; exists && existingUID != pod.UID {
		_ = connection.Close()
		archive.failLocked("archive_instance_reused")
		return errors.New("rolling: ledger instance name was reused")
	}
	if _, exists := archive.tracked[key]; exists {
		_ = connection.Close()
		return nil
	}
	if !initial && pod.Ready {
		archive.failLocked("archive_replacement_ready_before_discovery")
	}
	archive.instances[pod.Name] = dataCenter
	archive.seenUIDs[pod.Name] = pod.UID
	archive.tracked[key] = &trackedArchiveInstance{
		pod: pod, dataCenter: dataCenter, connection: connection,
	}

	return nil
}

func (archive *LedgerArchive) collect(
	ctx context.Context,
	instance *trackedArchiveInstance,
) (bool, error) {
	collector, err := probe.NewLedgerCollector([]probe.LedgerSource{{
		DataCenter: instance.dataCenter,
		Client:     instance.connection,
	}}, archive.config.LedgerLimit)
	if err != nil {
		return false, err
	}
	snapshot := collector.Collect(ctx)
	if !snapshot.IsComplete {
		return false, errors.New("rolling: direct ledger snapshot is incomplete")
	}
	for _, record := range snapshot.Records {
		if record.Source != instance.pod.Name || record.DataCenter != instance.dataCenter {
			return false, errors.New("rolling: direct ledger record identity mismatch")
		}
		key := record.Source + "\x00" + strconv.FormatUint(record.Sequence, 10)
		archive.mu.Lock()
		current, exists := archive.records[key]
		if exists && current != record {
			archive.failLocked("archive_record_conflict")
			archive.mu.Unlock()
			return false, errors.New("rolling: archived ledger record changed")
		}
		if !exists && len(archive.records) >= archive.config.RecordLimit {
			archive.failLocked("archive_record_capacity")
			archive.mu.Unlock()
			return false, errors.New("rolling: archived ledger record capacity exceeded")
		}
		archive.records[key] = record
		archive.mu.Unlock()
	}

	return true, nil
}

func (archive *LedgerArchive) signalReady(err error) {
	archive.readyOnce.Do(func() {
		archive.readyMu.Lock()
		archive.readyErr = err
		archive.readyMu.Unlock()
		close(archive.ready)
	})
}

func (archive *LedgerArchive) markIncomplete(reason string) {
	archive.mu.Lock()
	defer archive.mu.Unlock()
	archive.failLocked(reason)
}

func (archive *LedgerArchive) isHealthy() bool {
	archive.mu.RLock()
	defer archive.mu.RUnlock()
	return archive.healthy
}

func (archive *LedgerArchive) failLocked(reason string) {
	archive.healthy = false
	archive.incomplete[reason] = struct{}{}
}

func (archive *LedgerArchive) closeAll() error {
	archive.mu.Lock()
	connections := make([]archiveConnection, 0, len(archive.tracked))
	for key, instance := range archive.tracked {
		connections = append(connections, instance.connection)
		delete(archive.tracked, key)
	}
	archive.mu.Unlock()

	results := make(chan error, len(connections))
	for _, connection := range connections {
		go func() { results <- connection.Close() }()
	}
	var resultErr error
	for range connections {
		if err := <-results; err != nil {
			resultErr = errors.Join(resultErr, errors.New("rolling: closing ledger connection"))
		}
	}
	return resultErr
}

func validateLedgerArchiveConfig(config LedgerArchiveConfig) error {
	if err := validateRunID(config.RunID); err != nil {
		return err
	}
	if len(config.Clusters) != 2 {
		return errors.New("rolling: ledger archive requires two internal clusters")
	}
	seen := make(map[string]struct{}, 2)
	for _, cluster := range config.Clusters {
		if err := validateClusterHandoff(cluster); err != nil {
			return err
		}
		if cluster.Zone != "internal" {
			return errors.New("rolling: ledger archive accepts only internal clusters")
		}
		if _, exists := seen[cluster.DC]; exists {
			return errors.New("rolling: ledger archive data center is duplicated")
		}
		seen[cluster.DC] = struct{}{}
	}
	if config.PollInterval < 10*time.Millisecond || config.PollInterval > time.Second ||
		config.CallTimeout <= 0 || config.CallTimeout > 10*time.Second ||
		config.StopTimeout <= portForwardShutdownTimeout || config.StopTimeout > time.Minute ||
		config.LedgerLimit == 0 || config.LedgerLimit > 100_000 ||
		config.RecordLimit <= 0 || config.RecordLimit > 1_000_000 {
		return errors.New("rolling: ledger archive bounds are invalid")
	}

	return nil
}

func compareString(left string, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
