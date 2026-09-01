package rolling

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

// ExecutionConfig bounds one complete probe, rolling action, evidence archive,
// report, and artifact-publication lifecycle.
type ExecutionConfig struct {
	RunID             string
	Scenario          spec.Scenario
	ArtifactDirectory string
	TotalTimeout      time.Duration
	StartupTimeout    time.Duration
	StopTimeout       time.Duration
}

type trafficRunner interface {
	Run(ctx context.Context) (probe.Snapshot, error)
}

type ledgerArchive interface {
	Run(ctx context.Context) error
	WaitReady(ctx context.Context) error
	Snapshot() probe.InternalSnapshot
}

type scenarioAction func(ctx context.Context) error

type trafficRunResult struct {
	snapshot probe.Snapshot
	err      error
}

// Execute runs continuous traffic and immutable ledger archival around one
// bounded rolling action. It always attempts to publish a failed report when
// valid scenario evidence exists; successful return additionally requires a
// passing SLO report.
func Execute(
	ctx context.Context,
	config ExecutionConfig,
	traffic trafficRunner,
	archive ledgerArchive,
	closeTraffic func(),
	action scenarioAction,
) (probe.ReportResult, error) {
	if err := validateExecutionConfig(config); err != nil {
		return probe.ReportResult{}, err
	}
	if ctx == nil {
		return probe.ReportResult{}, errors.New("rolling: execution context must not be nil")
	}
	if isNilDependency(traffic) || isNilDependency(archive) || closeTraffic == nil || action == nil {
		return probe.ReportResult{}, errors.New("rolling: execution dependencies are required")
	}

	runCtx, cancelRun := context.WithTimeout(ctx, config.TotalTimeout)
	defer cancelRun()

	archiveCtx, cancelArchive := context.WithCancel(runCtx)
	archiveResult := make(chan error, 1)
	go func() {
		archiveResult <- archive.Run(archiveCtx)
	}()
	startupCtx, cancelStartup := context.WithTimeout(runCtx, config.StartupTimeout)
	archiveReadyErr := archive.WaitReady(startupCtx)
	cancelStartup()
	if archiveReadyErr != nil {
		cancelArchive()
		_ = waitError(archiveResult, config.StopTimeout)
		return probe.ReportResult{}, fmt.Errorf(
			"rolling: preparing ledger archive: %w",
			archiveReadyErr,
		)
	}

	trafficCtx, cancelTraffic := context.WithCancel(runCtx)
	trafficResults := make(chan trafficRunResult, 1)
	go func() {
		snapshot, err := traffic.Run(trafficCtx)
		trafficResults <- trafficRunResult{snapshot: snapshot, err: err}
	}()

	actionErr := action(runCtx)
	cancelTraffic()
	closeTraffic()
	client, trafficErr := waitTraffic(trafficResults, config.StopTimeout)

	cancelArchive()
	archiveErr := waitError(archiveResult, config.StopTimeout)
	internal := archive.Snapshot()

	cleanupComplete := actionErr == nil && trafficErr == nil && archiveErr == nil
	capacity := []spec.CapacityInterval{}
	if cleanupComplete && client.IsComplete && internal.IsComplete &&
		client.FinishedOffset > 0 {
		capacity = append(capacity, spec.CapacityInterval{
			StartedAt:             client.StartedAt,
			EndedAt:               client.StartedAt.Add(client.FinishedOffset),
			PhysicallyAvailableDC: 2,
		})
	}
	report, reportErr := probe.BuildReport(config.Scenario, probe.ReportInput{
		RunID:           config.RunID,
		Client:          client,
		Internal:        internal,
		Capacity:        capacity,
		Exclusions:      []spec.ExclusionInterval{},
		CleanupComplete: cleanupComplete,
	})
	if reportErr != nil {
		return probe.ReportResult{}, errors.Join(
			actionErr,
			trafficErr,
			archiveErr,
			fmt.Errorf("rolling: building probe report: %w", reportErr),
		)
	}
	artifactErr := probe.WriteArtifacts(config.ArtifactDirectory, report)
	var statusErr error
	if report.Report.Status != spec.ReportStatusPass {
		statusErr = errors.New("rolling: SLO report failed")
	}

	return report, errors.Join(actionErr, trafficErr, archiveErr, artifactErr, statusErr)
}

func validateExecutionConfig(config ExecutionConfig) error {
	if err := validateRunID(config.RunID); err != nil {
		return err
	}
	if err := spec.ValidateScenario(config.Scenario); err != nil {
		return fmt.Errorf("rolling: validating execution scenario: %w", err)
	}
	if config.ArtifactDirectory == "" || !filepath.IsAbs(config.ArtifactDirectory) ||
		filepath.Clean(config.ArtifactDirectory) != config.ArtifactDirectory {
		return errors.New("rolling: artifact directory must be a clean absolute path")
	}
	if config.TotalTimeout <= 0 || config.TotalTimeout > 2*time.Hour ||
		config.StartupTimeout <= 0 || config.StartupTimeout > 5*time.Minute ||
		config.StopTimeout <= 0 || config.StopTimeout > 5*time.Minute {
		return errors.New("rolling: execution timeout is outside bounds")
	}

	return nil
}

func waitTraffic(
	results <-chan trafficRunResult,
	timeout time.Duration,
) (probe.Snapshot, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.snapshot, result.err
	case <-timer.C:
		return probe.Snapshot{}, errors.New("rolling: probe stop timeout")
	}
}

func waitError(results <-chan error, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-results:
		return err
	case <-timer.C:
		return errors.New("rolling: ledger archive stop timeout")
	}
}
