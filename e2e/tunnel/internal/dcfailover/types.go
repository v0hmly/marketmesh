package dcfailover

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/pki"
)

const (
	// EnvironmentLocalE2E is the only environment authorized for destructive scenarios.
	EnvironmentLocalE2E = "local-e2e"
	maxPhaseTimeout     = 30 * time.Minute
)

// DC is one bounded failure domain in the disposable topology.
type DC string

const (
	// DCA is the first failure domain in the symmetric scenario.
	DCA DC = "dc-a"
	// DCB is the second failure domain in the symmetric scenario.
	DCB DC = "dc-b"
)

// Zone identifies one of the two cluster trust zones in a DC.
type Zone string

const (
	// ZoneDMZ identifies an external gateway cluster.
	ZoneDMZ Zone = "dmz"
	// ZoneInternal identifies an outbound gateway and service cluster.
	ZoneInternal Zone = "internal"
)

// OutageKind distinguishes a planned drain from an abrupt outage.
type OutageKind string

const (
	// OutageManaged drains routing before stopping clusters.
	OutageManaged OutageKind = "managed"
	// OutageSudden stops clusters before health exclusion completes.
	OutageSudden OutageKind = "sudden"
)

// MarkerPhase is a bounded, low-cardinality probe timeline marker.
type MarkerPhase string

const (
	// MarkerFaultStarted marks the beginning of an authorized fault.
	MarkerFaultStarted MarkerPhase = "fault_started"
	// MarkerDCExcluded marks front door exclusion of the affected DC.
	MarkerDCExcluded MarkerPhase = "dc_excluded"
	// MarkerDCRestored marks restored cluster lifecycle before readiness.
	MarkerDCRestored MarkerPhase = "dc_restored"
	// MarkerDCEligible marks completed controlled failback.
	MarkerDCEligible MarkerPhase = "dc_eligible"
)

// Cluster is an immutable exact resource selection returned by topology
// preflight. OwnerRunID must come from a verified runtime label, not a name.
type Cluster struct {
	DC             DC
	Zone           Zone
	Name           string
	Kubeconfig     string
	KubeContext    string
	OwnerRunID     string
	ContainerNames []string
	NetworkNames   []string
}

// Snapshot is the complete disposable topology authorized for this run.
type Snapshot struct {
	RunID       string
	Environment string
	Disposable  bool
	Clusters    []Cluster
}

// DCTarget contains the exact DMZ and internal resources of one DC.
type DCTarget struct {
	DC       DC
	Clusters []Cluster
}

// Marker is safe to serialize in the probe timeline.
type Marker struct {
	DC         DC
	OutageKind OutageKind
	Phase      MarkerPhase
}

// Config contains all orchestration limits and the unique run identity.
type Config struct {
	RunID           string
	PhaseTimeout    time.Duration
	FinalizeTimeout time.Duration
}

// Topology owns exact disposable cluster, container, and network lifecycle.
type Topology interface {
	Preflight(ctx context.Context, runID string) (Snapshot, error)
	StopDC(ctx context.Context, target DCTarget, kind OutageKind) error
	RestoreDC(ctx context.Context, target DCTarget) error
	Inspect(ctx context.Context, snapshot Snapshot) error
	Cleanup(ctx context.Context, snapshot Snapshot) error
}

// Drainer owns the managed pre-outage drain supplied by rolling lifecycle.
type Drainer interface {
	DrainDC(ctx context.Context, dc DC) error
}

// FrontDoor is the public MM-30 health refresh contract. The concrete
// frontdoor.FrontDoor satisfies it without a local routing implementation.
type FrontDoor interface {
	Check(ctx context.Context)
}

// Readiness verifies capacity, routing exclusion, and full restored readiness.
type Readiness interface {
	WaitBaseline(ctx context.Context) error
	WaitSurvivor(ctx context.Context, dc DC) error
	WaitExcluded(ctx context.Context, dc DC) error
	WaitRestored(ctx context.Context, dc DC) error
	WaitEligible(ctx context.Context, dc DC) error
}

// Probe owns bounded read and mutating request streams and ledger validation.
// Start's context bounds startup; the stream remains owned until Stop returns.
type Probe interface {
	Start(ctx context.Context) error
	Mark(ctx context.Context, marker Marker) error
	Stop(ctx context.Context) error
	Verify(ctx context.Context) error
}

func validateConfig(config Config) error {
	if err := pki.ValidateRunID(config.RunID); err != nil {
		return fmt.Errorf("dcfailover: validating run id: %w", err)
	}
	if err := validateTimeout("phase", config.PhaseTimeout); err != nil {
		return err
	}
	if err := validateTimeout("finalize", config.FinalizeTimeout); err != nil {
		return err
	}

	return nil
}

func validateTimeout(name string, value time.Duration) error {
	if value <= 0 || value > maxPhaseTimeout {
		return fmt.Errorf("dcfailover: %s timeout is outside bounds", name)
	}

	return nil
}

func validateSnapshot(snapshot Snapshot, runID string) (Snapshot, error) {
	if snapshot.RunID != runID {
		return Snapshot{}, errors.New("dcfailover: topology run id does not match")
	}
	if snapshot.Environment != EnvironmentLocalE2E || !snapshot.Disposable {
		return Snapshot{}, errors.New("dcfailover: topology is not disposable local e2e")
	}
	if len(snapshot.Clusters) != 4 {
		return Snapshot{}, errors.New("dcfailover: exactly four clusters are required")
	}

	clusters := make([]Cluster, 0, len(snapshot.Clusters))
	validator := snapshotValidator{
		seenZones:      make(map[string]struct{}, len(snapshot.Clusters)),
		seenNames:      make(map[string]struct{}, len(snapshot.Clusters)),
		seenTargets:    make(map[string]struct{}, len(snapshot.Clusters)),
		seenContainers: map[string]struct{}{},
		seenNetworks:   map[string]struct{}{},
	}
	for _, cluster := range snapshot.Clusters {
		validated, err := validator.validateCluster(cluster, runID)
		if err != nil {
			return Snapshot{}, err
		}
		clusters = append(clusters, validated)
	}

	return Snapshot{
		RunID:       snapshot.RunID,
		Environment: snapshot.Environment,
		Disposable:  snapshot.Disposable,
		Clusters:    clusters,
	}, nil
}

type snapshotValidator struct {
	seenZones      map[string]struct{}
	seenNames      map[string]struct{}
	seenTargets    map[string]struct{}
	seenContainers map[string]struct{}
	seenNetworks   map[string]struct{}
}

func (validator *snapshotValidator) validateCluster(cluster Cluster, runID string) (Cluster, error) {
	if cluster.DC != DCA && cluster.DC != DCB {
		return Cluster{}, errors.New("dcfailover: cluster dc must be dc-a or dc-b")
	}
	if cluster.Zone != ZoneDMZ && cluster.Zone != ZoneInternal {
		return Cluster{}, errors.New("dcfailover: cluster zone must be dmz or internal")
	}
	if cluster.OwnerRunID != runID {
		return Cluster{}, errors.New("dcfailover: cluster is not owned by this run")
	}
	if err := validateExactValue("cluster name", cluster.Name); err != nil {
		return Cluster{}, err
	}
	if err := validateExactValue("kubeconfig", cluster.Kubeconfig); err != nil {
		return Cluster{}, err
	}
	if err := validateExactValue("kube context", cluster.KubeContext); err != nil {
		return Cluster{}, err
	}
	if len(cluster.ContainerNames) == 0 || len(cluster.NetworkNames) == 0 {
		return Cluster{}, errors.New("dcfailover: exact container and network names are required")
	}

	zoneKey := string(cluster.DC) + "/" + string(cluster.Zone)
	if err := addUnique(validator.seenZones, zoneKey, "cluster zone"); err != nil {
		return Cluster{}, err
	}
	if err := addUnique(validator.seenNames, cluster.Name, "cluster name"); err != nil {
		return Cluster{}, err
	}
	targetKey := cluster.Kubeconfig + "\x00" + cluster.KubeContext
	if err := addUnique(validator.seenTargets, targetKey, "kubernetes target"); err != nil {
		return Cluster{}, err
	}
	if err := addUniqueValues(
		validator.seenContainers,
		cluster.ContainerNames,
		"container name",
	); err != nil {
		return Cluster{}, err
	}
	if err := addUniqueValues(validator.seenNetworks, cluster.NetworkNames, "network name"); err != nil {
		return Cluster{}, err
	}

	cluster.ContainerNames = slices.Clone(cluster.ContainerNames)
	cluster.NetworkNames = slices.Clone(cluster.NetworkNames)

	return cluster, nil
}

func validateExactValue(name, value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("dcfailover: exact %s is required", name)
	}
	if strings.ContainsAny(value, "*?[]") {
		return fmt.Errorf("dcfailover: %s must not contain glob characters", name)
	}

	return nil
}

func addUniqueValues(seen map[string]struct{}, values []string, name string) error {
	for _, value := range values {
		if err := validateExactValue(name, value); err != nil {
			return err
		}
		if err := addUnique(seen, value, name); err != nil {
			return err
		}
	}

	return nil
}

func addUnique(seen map[string]struct{}, value, name string) error {
	if _, found := seen[value]; found {
		return fmt.Errorf("dcfailover: %s %q is duplicated", name, value)
	}
	seen[value] = struct{}{}

	return nil
}

func targetForDC(snapshot Snapshot, dc DC) (DCTarget, error) {
	clusters := make([]Cluster, 0, 2)
	for _, cluster := range snapshot.Clusters {
		if cluster.DC == dc {
			clusters = append(clusters, cluster)
		}
	}
	if len(clusters) != 2 {
		return DCTarget{}, fmt.Errorf("dcfailover: %s does not contain exactly two clusters", dc)
	}
	slices.SortFunc(clusters, func(left, right Cluster) int {
		return strings.Compare(string(left.Zone), string(right.Zone))
	})

	return DCTarget{DC: dc, Clusters: clusters}, nil
}

func otherDC(dc DC) DC {
	if dc == DCA {
		return DCB
	}

	return DCA
}
