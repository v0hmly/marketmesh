package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/podchaos"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

const (
	defaultOverallTimeout = 15 * time.Minute
	maximumOverallTimeout = 15 * time.Minute
	operationTimeout      = 30 * time.Second
	diagnosticsTimeout    = 2 * time.Minute
	runnerStopTimeout     = 10 * time.Second
	runnerWaitTimeout     = 20 * time.Second
	requestTimeout        = 5 * time.Second
	streamRPS             = uint32(25)
	streamConcurrency     = 4
	streamQueueCapacity   = 256
	recordCapacity        = 50_000
	eventCapacity         = 150_000
	ledgerLimit           = 50_000
	runLifecycleEvents    = 3
	faultMarkerEvents     = 32
	diagnosticByteLimit   = 8 * 1024 * 1024
)

type options struct {
	runID         string
	inventoryPath string
	scenarioPath  string
	artifactsRoot string
	kubectlPath   string
	revision      string
	timeout       time.Duration
}

type runnerResult struct {
	snapshot probe.Snapshot
	err      error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "podchaos:", err)
		os.Exit(1)
	}
}

func run(
	parentCtx context.Context,
	arguments []string,
	stdout io.Writer,
	stderr io.Writer,
) (resultErr error) {
	configuration, err := parseOptions(arguments, stderr)
	if err != nil {
		return err
	}
	if parentCtx == nil || stdout == nil {
		return errors.New("runner context and stdout are required")
	}
	ctx, cancel := context.WithTimeout(parentCtx, configuration.timeout)
	defer cancel()
	resourceCtx, cancelResources := context.WithTimeout(
		context.WithoutCancel(parentCtx),
		configuration.timeout+diagnosticsTimeout,
	)
	resources := &runtimeResources{cancel: cancelResources}
	cleanupInvoked := false
	defer func() {
		if !cleanupInvoked {
			resultErr = errors.Join(resultErr, resources.Close())
		}
	}()

	inventory, scenario, execution, err := loadContracts(configuration)
	if err != nil {
		return err
	}
	steady, recoveryTimeout, err := runtimeSLO(scenario)
	if err != nil {
		return err
	}
	if err := requireNewRunDirectory(configuration.artifactsRoot, configuration.runID); err != nil {
		return err
	}

	routing, err := podchaos.NewKubectlRoutingReader(configuration.kubectlPath)
	if err != nil {
		return err
	}
	controller, err := podchaos.NewKubernetesController(podchaos.KubernetesControllerConfig{
		Targets: inventory.KubernetesTargets(), Routing: routing,
		KubectlPath: configuration.kubectlPath,
	})
	if err != nil {
		return err
	}
	collector, err := podchaos.NewKubernetesCollector(
		configuration.artifactsRoot,
		diagnosticByteLimit,
		configuration.kubectlPath,
	)
	if err != nil {
		return err
	}
	localFrontDoor, err := podchaos.StartLocalFrontDoor(resourceCtx, inventory)
	if err != nil {
		return err
	}
	resources.frontDoor = localFrontDoor

	ledgerCollector, err := resources.startLedgerCollector(
		resourceCtx,
		controller,
		configuration.runID,
		configuration.kubectlPath,
	)
	if err != nil {
		return err
	}
	discoveryCtx, cancelDiscovery := context.WithTimeout(ctx, operationTimeout)
	directory, err := ledgerCollector.Discover(discoveryCtx)
	cancelDiscovery()
	if err != nil {
		return err
	}
	invoker, err := probe.NewFrontDoorInvoker(localFrontDoor.Endpoint(), directory)
	if err != nil {
		return err
	}
	resources.invoker = invoker

	trafficConfiguration, err := trafficConfig(configuration.timeout)
	if err != nil {
		return err
	}
	trafficRunner, err := probe.New(trafficConfiguration, invoker, probe.Dependencies{})
	if err != nil {
		return err
	}
	adapter := &probeAdapter{
		timeline: trafficRunner, steady: steady, revision: configuration.revision,
	}
	faultScenario, err := podchaos.New(podchaos.Config{
		OperationTimeout:    operationTimeout,
		RecoveryTimeout:     recoveryTimeout,
		DiagnosticsTimeout:  diagnosticsTimeout,
		DeletionGracePeriod: 30 * time.Second,
	}, controller, adapter, collector)
	if err != nil {
		return err
	}

	trafficCtx, cancelTraffic := context.WithCancel(ctx)
	runnerDone := make(chan runnerResult, 1)
	go func() {
		snapshot, runErr := trafficRunner.Run(trafficCtx)
		runnerDone <- runnerResult{snapshot: snapshot, err: runErr}
	}()
	scenarioErr := faultScenario.Run(ctx, execution)
	cancelTraffic()
	trafficResult, waitErr := waitForRunner(runnerDone, runnerWaitTimeout)

	ledgerCtx, cancelLedger := context.WithTimeout(
		context.WithoutCancel(ctx),
		operationTimeout,
	)
	internal := ledgerCollector.Collect(ledgerCtx)
	cancelLedger()
	cleanupErr := resources.Close()
	cleanupInvoked = true
	cleanupComplete := cleanupErr == nil && waitErr == nil

	capacity := capacityEvidence(trafficResult.snapshot, scenario.WarmUp.Value())
	reportResult, reportErr := probe.BuildReport(scenario, probe.ReportInput{
		RunID: configuration.runID, Client: trafficResult.snapshot,
		Internal: internal, Capacity: capacity,
		Exclusions: []spec.ExclusionInterval{}, CleanupComplete: cleanupComplete,
	})
	if reportErr != nil {
		return errors.Join(scenarioErr, trafficResult.err, waitErr, cleanupErr, reportErr)
	}
	reportDirectory := filepath.Join(configuration.artifactsRoot, configuration.runID, "probe")
	artifactErr := probe.WriteArtifacts(reportDirectory, reportResult)
	textErr := probe.WriteTextReport(stdout, reportResult.Report)
	var statusErr error
	if reportResult.Report.Status != spec.ReportStatusPass {
		statusErr = errors.New("SLO report failed")
	}
	return errors.Join(
		scenarioErr,
		trafficResult.err,
		waitErr,
		cleanupErr,
		artifactErr,
		textErr,
		statusErr,
	)
}

func parseOptions(arguments []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("tunnel-podchaos", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var result options
	flags.StringVar(&result.runID, "run-id", "", "exact disposable MM-32 run ID")
	flags.StringVar(&result.inventoryPath, "inventory", "", "absolute MM-28 inventory path")
	flags.StringVar(&result.scenarioPath, "scenario", "", "absolute MM-27 scenario path")
	flags.StringVar(&result.artifactsRoot, "artifacts", "", "absolute new diagnostics root")
	flags.StringVar(&result.kubectlPath, "kubectl", "", "absolute pinned kubectl executable")
	flags.StringVar(&result.revision, "revision", "", "source revision marker")
	flags.DurationVar(&result.timeout, "timeout", defaultOverallTimeout, "bounded overall run timeout")
	if err := flags.Parse(arguments); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("positional arguments are not supported")
	}
	if result.runID == "" || result.inventoryPath == "" || result.scenarioPath == "" ||
		result.artifactsRoot == "" || result.kubectlPath == "" || !validRevision(result.revision) {
		return options{}, errors.New("all path, run-id, and revision flags are required")
	}
	if result.timeout <= 0 || result.timeout > maximumOverallTimeout {
		return options{}, errors.New("overall timeout is outside bounds")
	}
	for name, path := range map[string]string{
		"inventory": result.inventoryPath,
		"scenario":  result.scenarioPath,
		"kubectl":   result.kubectlPath,
	} {
		if err := validateRegularAbsolutePath(path); err != nil {
			return options{}, fmt.Errorf("%s path: %w", name, err)
		}
	}
	kubectlInfo, err := os.Lstat(result.kubectlPath)
	if err != nil || kubectlInfo.Mode().Perm()&0o111 == 0 {
		return options{}, errors.New("kubectl path must identify an executable file")
	}
	if !filepath.IsAbs(result.artifactsRoot) ||
		filepath.Clean(result.artifactsRoot) != result.artifactsRoot ||
		result.artifactsRoot == string(filepath.Separator) {
		return options{}, errors.New("artifacts path must be absolute, clean, and non-root")
	}
	return result, nil
}

func validateRegularAbsolutePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		path == string(filepath.Separator) {
		return errors.New("must be absolute, clean, and non-root")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("must identify a non-symlink regular file")
	}
	return nil
}

func validRevision(revision string) bool {
	if len(revision) == 0 || len(revision) > 64 {
		return false
	}
	for index, character := range revision {
		isAlphaNumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if !isAlphaNumeric && character != '-' && character != '_' && character != '.' {
			return false
		}
		if index == 0 && !isAlphaNumeric {
			return false
		}
	}
	return true
}

func loadContracts(
	configuration options,
) (podchaos.TopologyInventory, spec.Scenario, podchaos.Execution, error) {
	inventoryFile, err := os.Open(configuration.inventoryPath)
	if err != nil {
		return podchaos.TopologyInventory{}, spec.Scenario{}, podchaos.Execution{},
			errors.New("opening topology inventory")
	}
	inventory, decodeInventoryErr := podchaos.DecodeTopologyInventory(
		inventoryFile,
		configuration.runID,
	)
	inventoryCloseErr := inventoryFile.Close()
	if err := errors.Join(decodeInventoryErr, inventoryCloseErr); err != nil {
		return podchaos.TopologyInventory{}, spec.Scenario{}, podchaos.Execution{}, err
	}
	scenarioFile, err := os.Open(configuration.scenarioPath)
	if err != nil {
		return podchaos.TopologyInventory{}, spec.Scenario{}, podchaos.Execution{},
			errors.New("opening SLO scenario")
	}
	scenario, decodeScenarioErr := spec.DecodeScenario(scenarioFile)
	scenarioCloseErr := scenarioFile.Close()
	if err := errors.Join(decodeScenarioErr, scenarioCloseErr); err != nil {
		return podchaos.TopologyInventory{}, spec.Scenario{}, podchaos.Execution{}, err
	}
	execution := podchaos.DefaultExecution(configuration.runID)
	if err := podchaos.ValidateSLOScenario(scenario, execution); err != nil {
		return podchaos.TopologyInventory{}, spec.Scenario{}, podchaos.Execution{}, err
	}
	return inventory, scenario, execution, nil
}

func runtimeSLO(scenario spec.Scenario) (probe.SteadyRequirement, time.Duration, error) {
	var requirement probe.SteadyRequirement
	var recoveryTimeout time.Duration
	for _, fault := range scenario.Faults {
		if fault.Recovery == nil {
			return probe.SteadyRequirement{}, 0, errors.New("SLO recovery is required")
		}
		recoveryTimeout = max(recoveryTimeout, fault.Recovery.MaxDuration.Value())
		for _, class := range fault.Recovery.Classes {
			switch class {
			case spec.RequestClassReadIdempotent:
				requirement.ReadSuccesses = max(
					requirement.ReadSuccesses,
					fault.Recovery.SuccessStreak,
				)
			case spec.RequestClassMutating:
				requirement.MutatingSuccesses = max(
					requirement.MutatingSuccesses,
					fault.Recovery.SuccessStreak,
				)
			}
		}
	}
	if requirement.ReadSuccesses == 0 || requirement.MutatingSuccesses == 0 ||
		recoveryTimeout <= 0 || recoveryTimeout > 30*time.Minute {
		return probe.SteadyRequirement{}, 0, errors.New("SLO runtime bounds are invalid")
	}
	return requirement, recoveryTimeout, nil
}

func requireNewRunDirectory(root string, runID string) error {
	path := filepath.Join(root, runID)
	_, err := os.Lstat(path)
	if err == nil {
		return errors.New("run artifact directory already exists")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("checking run artifact directory")
	}
	return nil
}

func trafficConfig(runTimeout time.Duration) (probe.Config, error) {
	records, events, err := trafficUpperBounds(runTimeout)
	if err != nil {
		return probe.Config{}, err
	}
	if records > recordCapacity || events > eventCapacity {
		return probe.Config{}, errors.New("traffic journal capacity is below the run upper bound")
	}
	return probe.Config{
		RunTimeout: runTimeout, StopTimeout: runnerStopTimeout,
		RequestTimeout: requestTimeout,
		Read: probe.StreamConfig{
			RPS: streamRPS, Concurrency: streamConcurrency,
			QueueCapacity: streamQueueCapacity,
		},
		Mutating: probe.StreamConfig{
			RPS: streamRPS, Concurrency: streamConcurrency,
			QueueCapacity: streamQueueCapacity,
		},
		RecordCapacity: recordCapacity,
		EventCapacity:  eventCapacity,
	}, nil
}

func trafficUpperBounds(runTimeout time.Duration) (uint64, uint64, error) {
	if runTimeout <= 0 || runTimeout > maximumOverallTimeout {
		return 0, 0, errors.New("traffic run timeout is outside bounds")
	}
	ticksPerStream := (uint64(runTimeout)*uint64(streamRPS) + uint64(time.Second) - 1) /
		uint64(time.Second)
	records := 2 * (ticksPerStream + 1)
	events := 3*records + runLifecycleEvents + faultMarkerEvents
	return records, events, nil
}

func waitForRunner(done <-chan runnerResult, timeout time.Duration) (runnerResult, error) {
	if done == nil || timeout <= 0 {
		return runnerResult{}, errors.New("traffic runner wait input is invalid")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result, nil
	case <-timer.C:
		return runnerResult{
			snapshot: probe.Snapshot{
				IsComplete: false, IncompleteReasons: []string{"scenario_runner_wait_timeout"},
			},
		}, errors.New("waiting for traffic runner shutdown")
	}
}

func capacityEvidence(snapshot probe.Snapshot, warmUp time.Duration) []spec.CapacityInterval {
	startedAt := snapshot.StartedAt.Add(warmUp)
	endedAt := snapshot.StartedAt.Add(snapshot.FinishedOffset)
	if snapshot.StartedAt.IsZero() || warmUp < 0 || !endedAt.After(startedAt) {
		return []spec.CapacityInterval{}
	}
	return []spec.CapacityInterval{{
		StartedAt: startedAt, EndedAt: endedAt, PhysicallyAvailableDC: 2,
	}}
}
