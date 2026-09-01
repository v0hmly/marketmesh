// Command e2e-topology manages the disposable MM-28 Kubernetes topology.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	flags := flag.NewFlagSet("e2e-topology", flag.ContinueOnError)
	instance := flags.String("instance", "mm28", "unique disposable resource prefix")
	dockerContext := flags.String("docker-context", "orbstack", "explicit Docker context")
	debug := flags.Bool("debug", false, "enable debug logs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: e2e-topology [flags] {bootstrap|down|inspect|inventory|ready|up|verify|versions}")
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

	switch flags.Arg(0) {
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
		fmt.Println(string(encoded))
		return nil
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
		fmt.Println(string(encoded))
		return nil
	default:
		return errors.New("usage: e2e-topology [flags] {bootstrap|down|inspect|inventory|ready|up|verify|versions}")
	}
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
