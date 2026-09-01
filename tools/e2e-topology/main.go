// Command e2e-topology manages the disposable MM-28 Kubernetes topology.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/v0hmly/marketmesh/tools/e2e-topology/internal/topology"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		logger.Error("topology command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runWithIO(args, os.Stdin, os.Stdout)
}

func runWithIO(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("e2e-topology", flag.ContinueOnError)
	instance := flags.String("instance", "mm28", "unique disposable resource prefix")
	dockerContext := flags.String("docker-context", "orbstack", "explicit Docker context")
	debug := flags.Bool("debug", false, "enable debug logs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return usageError()
	}

	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	config, err := topology.NewConfig(root, *instance, *dockerContext)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	runner := topology.NewExecRunner(logger)
	manager := topology.New(config, runner, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := flags.Arg(0)
	commandArgs := flags.Args()[1:]
	if command != "targets" && len(commandArgs) != 0 {
		return usageError()
	}
	switch command {
	case "bootstrap":
		return topology.NewBootstrapper(config, runner, logger).Bootstrap(ctx)
	case "down":
		return manager.Down(ctx)
	case "inspect":
		return manager.Inspect(ctx)
	case "inventory":
		inventory, inventoryErr := manager.Inventory(ctx)
		if inventoryErr != nil {
			return inventoryErr
		}
		encoded, encodeErr := json.MarshalIndent(inventory, "", "  ")
		if encodeErr != nil {
			return fmt.Errorf("encoding topology inventory: %w", encodeErr)
		}
		return writeJSON(stdout, encoded)
	case "ready":
		return manager.Ready(ctx)
	case "up":
		return manager.Up(ctx)
	case "verify":
		return manager.Verify(ctx)
	case "versions":
		versions, versionsErr := manager.Versions(ctx)
		if versionsErr != nil {
			return versionsErr
		}
		encoded, encodeErr := json.MarshalIndent(versions, "", "  ")
		if encodeErr != nil {
			return fmt.Errorf("encoding topology versions: %w", encodeErr)
		}
		return writeJSON(stdout, encoded)
	case "targets":
		return runTargets(ctx, manager, commandArgs, commandStreams{stdin: stdin, stdout: stdout})
	default:
		return usageError()
	}
}

func runTargets(
	ctx context.Context,
	manager *topology.Topology,
	args []string,
	streams commandStreams,
) error {
	if len(args) == 0 {
		return errors.New("usage: e2e-topology [flags] targets {rebind|resolve|validate} [target flags]")
	}
	switch args[0] {
	case "rebind":
		flags := flag.NewFlagSet("targets rebind", flag.ContinueOnError)
		transitionSource := flags.String("transition", "", "transition source; only - (stdin) is accepted")
		target := flags.String("target", "", "exact logical target")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *target == "" {
			return errors.New("topology: targets rebind requires one exact --target and no extra arguments")
		}
		if *transitionSource != "-" {
			return errors.New("topology: targets rebind requires --transition - to avoid path races")
		}
		input, err := topology.DecodeTargetRebindInput(streams.stdin)
		if err != nil {
			return err
		}
		result, err := manager.RebindTarget(ctx, input, *target)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding target rebind result: %w", err)
		}
		return writeJSON(streams.stdout, encoded)
	case "resolve":
		flags := flag.NewFlagSet("targets resolve", flag.ContinueOnError)
		consumerTask := flags.String("consumer-task", "", "exact MM task key")
		consumerRunID := flags.String("consumer-run-id", "", "unique disposable consumer run id")
		dc := flags.String("dc", "", "optional exact data center selector")
		zone := flags.String("zone", "", "optional exact zone selector")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("topology: targets resolve received unexpected arguments")
		}
		snapshot, err := manager.ResolveTargets(ctx, topology.TargetResolveRequest{
			ConsumerTask:  *consumerTask,
			ConsumerRunID: *consumerRunID,
			Selector: topology.TargetSelector{
				DC:   *dc,
				Zone: *zone,
			},
		})
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding target snapshot: %w", err)
		}
		return writeJSON(streams.stdout, encoded)
	case "validate":
		flags := flag.NewFlagSet("targets validate", flag.ContinueOnError)
		snapshotSource := flags.String("snapshot", "", "snapshot source; only - (stdin) is accepted")
		expectedState := flags.String("expected-state", "", "required running or stopped state")
		targets := stringListFlag{}
		flags.Var(&targets, "target", "exact logical target; repeat to validate a subset")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("topology: targets validate received unexpected arguments")
		}
		if *snapshotSource != "-" {
			return errors.New("topology: targets validate requires --snapshot - to avoid path races")
		}
		snapshot, err := topology.DecodeTargetSnapshot(streams.stdin)
		if err != nil {
			return err
		}
		receipt, err := manager.ValidateTargets(ctx, snapshot, topology.TargetValidateRequest{
			ExpectedState: topology.ExpectedTargetState(*expectedState),
			LogicalNames:  []string(targets),
		})
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding target validation receipt: %w", err)
		}
		return writeJSON(streams.stdout, encoded)
	default:
		return errors.New("usage: e2e-topology [flags] targets {rebind|resolve|validate} [target flags]")
	}
}

type commandStreams struct {
	stdin  io.Reader
	stdout io.Writer
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return fmt.Sprint([]string(*f))
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func writeJSON(writer io.Writer, encoded []byte) error {
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("writing command output: %w", err)
	}
	return nil
}

func usageError() error {
	return errors.New(
		"usage: e2e-topology [flags] {bootstrap|down|inspect|inventory|ready|targets|up|verify|versions}",
	)
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("reading working directory: %w", err)
	}
	for {
		taskfile := filepath.Join(directory, "Taskfile.yml")
		module := filepath.Join(directory, "tools", "e2e-topology", "go.mod")
		if regularFile(taskfile) && regularFile(module) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("topology: repository root was not found")
		}
		directory = parent
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
