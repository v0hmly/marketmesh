// Package rolling owns the bounded rolling redeploy and rollback scenario for MM-34.
package rolling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	Namespace = "marketmesh-e2e-tunnel"

	GatewayInDeployment    = "mm29-gateway-in"
	GatewayOutDeployment   = "mm29-gateway-out"
	FakeInternalDeployment = "mm29-fake-internal"

	ownerConfigMap            = "mm29-run-owner"
	configRevisionAnnotation  = "marketmesh.io/mm34-config-revision"
	rolloutRevisionAnnotation = "marketmesh.io/mm34-revision"
	minimumReplicas           = int32(2)
	minimumTerminationGrace   = int64(30)
	maximumShutdownTimeout    = 20 * time.Second
)

var (
	revisionPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?$`)
	digestPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[0-9a-f]{64}$`)

	// ErrReadinessNotReached identifies the expected failure mode of a negative rollout.
	ErrReadinessNotReached = errors.New("rolling: readiness was not reached")
)

// Variant defines one deterministic DC and component order.
type Variant string

const (
	VariantA Variant = "a"
	VariantB Variant = "b"
)

// Component is a finite workload category used by markers and plans.
type Component string

const (
	ComponentGatewayIn    Component = "gateway-in"
	ComponentGatewayOut   Component = "gateway-out"
	ComponentFakeInternal Component = "fake-internal"
)

// ChangeKind identifies which adjacent revision is applied.
type ChangeKind string

const (
	ChangeImage  ChangeKind = "image"
	ChangeConfig ChangeKind = "config"
)

// Phase is a bounded lifecycle marker category.
type Phase string

const (
	PhaseSteady    Phase = "steady"
	PhaseRollout   Phase = "rollout"
	PhaseRollback  Phase = "rollback"
	PhaseRecovered Phase = "recovered"
)

// Result is a bounded lifecycle result category.
type Result string

const (
	ResultStarted Result = "started"
	ResultPassed  Result = "passed"
	ResultFailed  Result = "failed"
)

// Target is one allowlisted Deployment in one DC.
type Target struct {
	DC         string
	Zone       string
	Component  Component
	Deployment string
	Container  string
}

// Change describes one image or config-only Pod template revision.
type Change struct {
	Kind           ChangeKind
	Revision       string
	Image          string
	ConfigRevision string
}

// Fault requests the built-in fail-closed readiness fault for one target.
type Fault struct {
	Revision string
}

// Step mutates exactly one target.
type Step struct {
	Target Target
	Change Change
}

// Plan is one complete deterministic order. Run variants from fresh baselines.
type Plan struct {
	Variant Variant
	Steps   []Step
}

// Transition contains the two adjacent revisions for a component.
type Transition struct {
	Image          string
	ImageRevision  string
	ConfigRevision string
}

// Marker is intentionally finite and contains no payload, secret, or image reference.
type Marker struct {
	RunID     string
	DC        string
	Zone      string
	Component Component
	Phase     Phase
	Result    Result
	Revision  string
	Offset    time.Duration
}

// Snapshot records the exact safe fields required to restore a Deployment.
type Snapshot struct {
	UID            string
	Revision       int64
	Generation     int64
	Desired        int32
	Image          string
	ConfigRevision string
}

// Expectation is the Pod template state a rollout must converge to.
type Expectation struct {
	UID            string
	Image          string
	ConfigRevision string
	Desired        int32
}

// Cluster is one explicit Kubernetes boundary. Ambient kubeconfig is never used.
type Cluster struct {
	DC         string
	Zone       string
	Kubeconfig string
	Context    string
}

// Probe is implemented by the MM-31 adapter. Its implementation owns traffic.
type Probe interface {
	Mark(marker Marker) error
	WaitSteady(ctx context.Context, target Target) error
}

// Kubernetes owns only allowlisted rollout operations and diagnostics.
type Kubernetes interface {
	Prepare(ctx context.Context) error
	Preflight(ctx context.Context, target Target) (Snapshot, error)
	Update(ctx context.Context, target Target, change Change, snapshot Snapshot) error
	InjectReadinessFault(ctx context.Context, target Target, fault Fault, snapshot Snapshot) error
	Wait(ctx context.Context, target Target, expectation Expectation) error
	Diagnostics(ctx context.Context, target Target) error
	Rollback(ctx context.Context, target Target, revision string, snapshot Snapshot) error
}

// Config bounds every state-machine stage independently.
type Config struct {
	RunID              string
	TotalTimeout       time.Duration
	StepTimeout        time.Duration
	SteadyTimeout      time.Duration
	DiagnosticsTimeout time.Duration
	RollbackTimeout    time.Duration
	Output             io.Writer
	Now                func() time.Time
}

func validateRunID(value string) error {
	if !revisionPattern.MatchString(value) || len(value) > 63 {
		return errors.New("rolling: run id is outside bounds")
	}

	return nil
}

func validateRevision(value string) error {
	if !revisionPattern.MatchString(value) {
		return errors.New("rolling: revision is outside bounds")
	}

	return nil
}

func validateChange(change Change) error {
	if err := validateRevision(change.Revision); err != nil {
		return err
	}
	switch change.Kind {
	case ChangeImage:
		if len(change.Image) > 512 || !digestPattern.MatchString(change.Image) {
			return errors.New("rolling: image must use an immutable sha256 digest")
		}
		if change.ConfigRevision != "" {
			return errors.New("rolling: image change cannot set config revision")
		}
	case ChangeConfig:
		if change.Image != "" {
			return errors.New("rolling: config change cannot set image")
		}
		if err := validateRevision(change.ConfigRevision); err != nil {
			return fmt.Errorf("rolling: validating config revision: %w", err)
		}
	default:
		return errors.New("rolling: unknown change kind")
	}

	return nil
}

func validateTarget(target Target) error {
	expected, found := targetFor(target.DC, target.Component)
	if !found || expected != target {
		return errors.New("rolling: target is outside the MM-29 allowlist")
	}

	return nil
}

func targetFor(dc string, component Component) (Target, bool) {
	target := Target{DC: dc, Component: component}
	if dc != "dc-a" && dc != "dc-b" {
		return Target{}, false
	}
	switch component {
	case ComponentGatewayIn:
		target.Zone = "dmz"
		target.Deployment = GatewayInDeployment
		target.Container = "gateway-in"
	case ComponentGatewayOut:
		target.Zone = "internal"
		target.Deployment = GatewayOutDeployment
		target.Container = "gateway-out"
	case ComponentFakeInternal:
		target.Zone = "internal"
		target.Deployment = FakeInternalDeployment
		target.Container = "fake-internal"
	default:
		return Target{}, false
	}

	return target, true
}

func expectationFromSnapshot(snapshot Snapshot) Expectation {
	return Expectation{
		UID:            snapshot.UID,
		Image:          snapshot.Image,
		ConfigRevision: snapshot.ConfigRevision,
		Desired:        snapshot.Desired,
	}
}

func expectationFromChange(snapshot Snapshot, change Change) Expectation {
	expectation := expectationFromSnapshot(snapshot)
	if change.Kind == ChangeImage {
		expectation.Image = change.Image
	} else {
		expectation.ConfigRevision = change.ConfigRevision
	}

	return expectation
}

func safeRevision(value string) string {
	value = strings.TrimSpace(value)
	if revisionPattern.MatchString(value) {
		return value
	}

	return "invalid"
}
