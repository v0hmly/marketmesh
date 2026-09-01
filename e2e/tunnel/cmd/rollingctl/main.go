package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/rolling"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

const maximumCLIProbeRecords = 25_000

type options struct {
	runID             string
	mode              string
	inventoryPath     string
	frontDoorEndpoint string
	artifactDirectory string

	gatewayInImage    string
	gatewayOutImage   string
	fakeInternalImage string

	gatewayInImageRevision     string
	gatewayOutImageRevision    string
	fakeInternalImageRevision  string
	gatewayInConfigRevision    string
	gatewayOutConfigRevision   string
	fakeInternalConfigRevision string

	totalTimeout        time.Duration
	stepTimeout         time.Duration
	steadyTimeout       time.Duration
	diagnosticsTimeout  time.Duration
	rollbackTimeout     time.Duration
	startupTimeout      time.Duration
	stopTimeout         time.Duration
	requestTimeout      time.Duration
	archivePoll         time.Duration
	archiveCallTimeout  time.Duration
	readRPS             uint
	mutatingRPS         uint
	readConcurrency     int
	mutatingConcurrency int
	steadyRead          uint
	steadyMutating      uint
	ledgerLimit         uint
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	configuration, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	trafficConfig, err := probeConfig(configuration)
	if err != nil {
		return err
	}
	clusters, err := readInventory(configuration.inventoryPath)
	if err != nil {
		return err
	}
	kube, err := rolling.NewKubernetes(rolling.KubernetesConfig{
		RunID: configuration.runID, Clusters: clusters, Output: stdout,
	})
	if err != nil {
		return err
	}
	internalClusters := make([]rolling.Cluster, 0, 2)
	for _, cluster := range clusters {
		if cluster.Zone == "internal" {
			internalClusters = append(internalClusters, cluster)
		}
	}
	archive, err := rolling.NewLedgerArchive(rolling.LedgerArchiveConfig{
		RunID: configuration.runID, Clusters: internalClusters,
		PollInterval: configuration.archivePoll,
		CallTimeout:  configuration.archiveCallTimeout,
		StopTimeout:  configuration.stopTimeout,
		LedgerLimit:  uint32(configuration.ledgerLimit), // #nosec G115 -- bounded by parseOptions
		RecordLimit:  trafficConfig.RecordCapacity,
	})
	if err != nil {
		return err
	}
	frontDoor, err := probe.NewFrontDoorInvokerWithResolver(
		configuration.frontDoorEndpoint,
		archive,
	)
	if err != nil {
		return err
	}
	defer frontDoor.Close()
	traffic, err := probe.New(trafficConfig, frontDoor, probe.Dependencies{})
	if err != nil {
		return err
	}
	continuous, err := rolling.NewContinuousProbe(rolling.ContinuousProbeConfig{
		RunID:             configuration.runID,
		ReadSuccesses:     uint32(configuration.steadyRead),     // #nosec G115 -- bounded by probeConfig
		MutatingSuccesses: uint32(configuration.steadyMutating), // #nosec G115 -- bounded by probeConfig
	}, traffic)
	if err != nil {
		return err
	}
	runner, err := rolling.NewRunner(rolling.Config{
		RunID: configuration.runID, TotalTimeout: configuration.totalTimeout,
		StepTimeout:        configuration.stepTimeout,
		SteadyTimeout:      configuration.steadyTimeout,
		DiagnosticsTimeout: configuration.diagnosticsTimeout,
		RollbackTimeout:    configuration.rollbackTimeout,
		Output:             stdout,
	}, kube, continuous)
	if err != nil {
		return err
	}

	scenario, action, err := scenarioAction(configuration, runner)
	if err != nil {
		return err
	}
	result, executeErr := rolling.Execute(
		ctx,
		rolling.ExecutionConfig{
			RunID: configuration.runID, Scenario: scenario,
			ArtifactDirectory: configuration.artifactDirectory,
			TotalTimeout:      configuration.totalTimeout,
			StartupTimeout:    configuration.startupTimeout,
			StopTimeout:       configuration.stopTimeout,
		},
		traffic,
		archive,
		frontDoor.Close,
		action,
	)
	if result.Report.Status != "" {
		_, _ = fmt.Fprintf(
			stdout,
			"rolling: scenario=%s status=%s artifacts=%s\n",
			result.Report.ScenarioID,
			result.Report.Status,
			configuration.artifactDirectory,
		)
	}

	return executeErr
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("rollingctl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	result := options{}
	flags.StringVar(&result.runID, "run-id", "", "exact MM-29 workload run identifier")
	flags.StringVar(&result.mode, "mode", "", "scenario mode: a, b, or rollback")
	flags.StringVar(&result.inventoryPath, "inventory", "", "absolute MM-28 inventory JSON path")
	flags.StringVar(
		&result.frontDoorEndpoint,
		"frontdoor",
		"",
		"literal loopback MM-30 front-door URL",
	)
	flags.StringVar(
		&result.artifactDirectory,
		"artifacts",
		"",
		"new absolute artifact bundle directory",
	)
	flags.StringVar(&result.gatewayInImage, "gateway-in-image", "", "immutable adjacent image digest")
	flags.StringVar(&result.gatewayOutImage, "gateway-out-image", "", "immutable adjacent image digest")
	flags.StringVar(&result.fakeInternalImage, "fake-internal-image", "", "immutable adjacent image digest")
	flags.StringVar(
		&result.gatewayInImageRevision,
		"gateway-in-image-revision",
		"gateway-in-image-v2",
		"bounded gateway-in image revision marker",
	)
	flags.StringVar(
		&result.gatewayOutImageRevision,
		"gateway-out-image-revision",
		"gateway-out-image-v2",
		"bounded gateway-out image revision marker",
	)
	flags.StringVar(
		&result.fakeInternalImageRevision,
		"fake-internal-image-revision",
		"fake-internal-image-v2",
		"bounded fake-internal image revision marker",
	)
	flags.StringVar(
		&result.gatewayInConfigRevision,
		"gateway-in-config-revision",
		"gateway-in-config-v2",
		"bounded gateway-in config revision",
	)
	flags.StringVar(
		&result.gatewayOutConfigRevision,
		"gateway-out-config-revision",
		"gateway-out-config-v2",
		"bounded gateway-out config revision",
	)
	flags.StringVar(
		&result.fakeInternalConfigRevision,
		"fake-internal-config-revision",
		"fake-internal-config-v2",
		"bounded fake-internal config revision",
	)
	flags.DurationVar(&result.totalTimeout, "total-timeout", 30*time.Minute, "complete scenario timeout")
	flags.DurationVar(&result.stepTimeout, "step-timeout", 3*time.Minute, "rollout stage timeout")
	flags.DurationVar(&result.steadyTimeout, "steady-timeout", 30*time.Second, "steady traffic timeout")
	flags.DurationVar(
		&result.diagnosticsTimeout,
		"diagnostics-timeout",
		30*time.Second,
		"bounded diagnostics timeout",
	)
	flags.DurationVar(&result.rollbackTimeout, "rollback-timeout", 3*time.Minute, "rollback timeout")
	flags.DurationVar(&result.startupTimeout, "startup-timeout", time.Minute, "archive startup timeout")
	flags.DurationVar(&result.stopTimeout, "stop-timeout", 20*time.Second, "local component stop timeout")
	flags.DurationVar(&result.requestTimeout, "request-timeout", 5*time.Second, "probe request timeout")
	flags.DurationVar(&result.archivePoll, "archive-poll", 100*time.Millisecond, "ledger archive poll interval")
	flags.DurationVar(
		&result.archiveCallTimeout,
		"archive-call-timeout",
		10*time.Second,
		"one complete archive refresh timeout",
	)
	flags.UintVar(&result.readRPS, "read-rps", 5, "bounded read requests per second")
	flags.UintVar(&result.mutatingRPS, "mutating-rps", 5, "bounded mutating requests per second")
	flags.IntVar(&result.readConcurrency, "read-concurrency", 2, "read worker count")
	flags.IntVar(&result.mutatingConcurrency, "mutating-concurrency", 2, "mutating worker count")
	flags.UintVar(&result.steadyRead, "steady-read", 10, "required consecutive read successes")
	flags.UintVar(&result.steadyMutating, "steady-mutating", 10, "required consecutive mutating successes")
	flags.UintVar(&result.ledgerLimit, "ledger-limit", 10_000, "maximum entries per direct Pod ledger")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("rollingctl: parsing flags: %w", err)
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("rollingctl: positional arguments are not supported")
	}
	if result.runID == "" || result.inventoryPath == "" ||
		result.frontDoorEndpoint == "" || result.artifactDirectory == "" {
		return options{}, errors.New("rollingctl: run-id, inventory, frontdoor, and artifacts are required")
	}
	if !filepath.IsAbs(result.inventoryPath) ||
		filepath.Clean(result.inventoryPath) != result.inventoryPath ||
		!filepath.IsAbs(result.artifactDirectory) ||
		filepath.Clean(result.artifactDirectory) != result.artifactDirectory {
		return options{}, errors.New("rollingctl: inventory and artifacts must be clean absolute paths")
	}
	if result.mode != "a" && result.mode != "b" && result.mode != "rollback" {
		return options{}, errors.New("rollingctl: mode must be a, b, or rollback")
	}
	if result.mode != "rollback" &&
		(result.gatewayInImage == "" || result.gatewayOutImage == "" || result.fakeInternalImage == "") {
		return options{}, errors.New("rollingctl: all three immutable image transitions are required")
	}
	if result.ledgerLimit == 0 || result.ledgerLimit > 100_000 {
		return options{}, errors.New("rollingctl: ledger limit is outside bounds")
	}

	return result, nil
}

func readInventory(path string) ([]rolling.Cluster, error) {
	// #nosec G304 -- parseOptions requires a clean absolute operator-supplied
	// path; DecodeTopologyInventory then validates MM-28 schema and ownership.
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("rollingctl: opening topology inventory")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("rollingctl: topology inventory is not a regular file")
	}
	clusters, err := rolling.DecodeTopologyInventory(file)
	if err != nil {
		return nil, err
	}

	return clusters, nil
}

func probeConfig(configuration options) (probe.Config, error) {
	if configuration.totalTimeout <= 0 || configuration.totalTimeout > 2*time.Hour {
		return probe.Config{}, errors.New("rollingctl: total timeout is outside bounds")
	}
	if configuration.readRPS == 0 || configuration.mutatingRPS == 0 ||
		configuration.readRPS > 100_000 || configuration.mutatingRPS > 100_000 {
		return probe.Config{}, errors.New("rollingctl: both bounded traffic streams are required")
	}
	if configuration.readRPS > math.MaxUint32 || configuration.mutatingRPS > math.MaxUint32 ||
		configuration.steadyRead > math.MaxUint32 ||
		configuration.steadyMutating > math.MaxUint32 {
		return probe.Config{}, errors.New("rollingctl: traffic values exceed uint32")
	}
	if configuration.steadyRead == 0 || configuration.steadyMutating == 0 {
		return probe.Config{}, errors.New("rollingctl: both steady success streaks are required")
	}
	// #nosec G115 -- totalTimeout is positive and bounded to two hours above.
	seconds := uint64((configuration.totalTimeout + time.Second - 1) / time.Second)
	rps := uint64(configuration.readRPS) + uint64(configuration.mutatingRPS)
	if seconds == 0 || rps > maximumCLIProbeRecords ||
		seconds > maximumCLIProbeRecords/rps {
		return probe.Config{}, errors.New("rollingctl: probe plan exceeds artifact-safe record capacity")
	}
	// #nosec G115 -- the preceding division proves seconds*rps <= 25_000.
	recordCapacity := int(seconds*rps) + configuration.readConcurrency +
		configuration.mutatingConcurrency + 2
	if recordCapacity <= 0 || recordCapacity > maximumCLIProbeRecords {
		return probe.Config{}, errors.New("rollingctl: probe record capacity is outside bounds")
	}
	eventCapacity := recordCapacity*3 + 128
	queueCapacity := func(rps uint) int {
		return max(int(rps), 1) * 2
	}

	return probe.Config{
		RunTimeout: configuration.totalTimeout, StopTimeout: configuration.stopTimeout,
		RequestTimeout: configuration.requestTimeout,
		Read: probe.StreamConfig{
			RPS: uint32(configuration.readRPS), Concurrency: configuration.readConcurrency,
			QueueCapacity: queueCapacity(configuration.readRPS),
		},
		Mutating: probe.StreamConfig{
			RPS: uint32(configuration.mutatingRPS), Concurrency: configuration.mutatingConcurrency,
			QueueCapacity: queueCapacity(configuration.mutatingRPS),
		},
		RecordCapacity: recordCapacity, EventCapacity: eventCapacity,
	}, nil
}

func scenarioAction(
	configuration options,
	runner *rolling.Runner,
) (spec.Scenario, func(context.Context) error, error) {
	switch configuration.mode {
	case "a", "b":
		plan, err := rolling.NewPlan(
			rolling.Variant(configuration.mode),
			transitions(configuration),
		)
		if err != nil {
			return spec.Scenario{}, nil, err
		}
		scenario, err := rolling.ScenarioForPlan(plan)
		if err != nil {
			return spec.Scenario{}, nil, err
		}

		return scenario, func(ctx context.Context) error {
			return runner.Run(ctx, plan)
		}, nil
	case "rollback":
		scenario, err := rolling.ScenarioForRollback()
		if err != nil {
			return spec.Scenario{}, nil, err
		}

		return scenario, func(ctx context.Context) error {
			for _, target := range rolling.RollbackTargets() {
				err := runner.VerifyRollback(ctx, target, rolling.Fault{
					Revision: rollbackRevision(target),
				})
				if err != nil {
					return err
				}
			}
			return nil
		}, nil
	default:
		return spec.Scenario{}, nil, errors.New("rollingctl: unsupported scenario mode")
	}
}

func transitions(configuration options) map[rolling.Component]rolling.Transition {
	return map[rolling.Component]rolling.Transition{
		rolling.ComponentGatewayIn: {
			Image:          configuration.gatewayInImage,
			ImageRevision:  configuration.gatewayInImageRevision,
			ConfigRevision: configuration.gatewayInConfigRevision,
		},
		rolling.ComponentGatewayOut: {
			Image:          configuration.gatewayOutImage,
			ImageRevision:  configuration.gatewayOutImageRevision,
			ConfigRevision: configuration.gatewayOutConfigRevision,
		},
		rolling.ComponentFakeInternal: {
			Image:          configuration.fakeInternalImage,
			ImageRevision:  configuration.fakeInternalImageRevision,
			ConfigRevision: configuration.fakeInternalConfigRevision,
		},
	}
}

func rollbackRevision(target rolling.Target) string {
	return strings.Join([]string{"mm34", target.DC, string(target.Component), "unready"}, "-")
}
