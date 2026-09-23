package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/workload"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "workloadctl: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, stdout io.Writer, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("expected deploy, inspect, or undeploy")
	}
	command := arguments[0]
	if command != "deploy" && command != "inspect" && command != "undeploy" {
		return fmt.Errorf("unknown command %q", command)
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	values := bindFlags(flags)
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	if values.runID == "" {
		return errors.New("--run-id is required")
	}
	if command == "deploy" &&
		(values.gatewayInImage == "" || values.gatewayOutImage == "" || values.fakeInternalImage == "") {
		return errors.New("all three image flags are required for deploy")
	}

	manager, err := workload.New(workload.Config{
		RunID: values.runID, Version: values.version,
		GatewayInImage:    values.gatewayInImage,
		GatewayOutImage:   values.gatewayOutImage,
		FakeInternalImage: values.fakeInternalImage,
		Timeout:           values.timeout,
		Output:            stdout,
		Clusters: []workload.Cluster{
			{
				DC: "dc-a", Zone: "dmz", Kubeconfig: values.dcADMZKubeconfig,
				Context: values.dcADMZContext,
			},
			{
				DC: "dc-a", Zone: "internal", Kubeconfig: values.dcAInternalKubeconfig,
				Context: values.dcAInternalContext, GatewayInTarget: values.dcAGatewayInTarget,
			},
			{
				DC: "dc-b", Zone: "dmz", Kubeconfig: values.dcBDMZKubeconfig,
				Context: values.dcBDMZContext,
			},
			{
				DC: "dc-b", Zone: "internal", Kubeconfig: values.dcBInternalKubeconfig,
				Context: values.dcBInternalContext, GatewayInTarget: values.dcBGatewayInTarget,
			},
		},
	})
	if err != nil {
		return err
	}

	switch command {
	case "deploy":
		return manager.Deploy(ctx)
	case "inspect":
		return manager.Inspect(ctx)
	case "undeploy":
		return manager.Undeploy(ctx)
	default:
		return errors.New("unreachable command")
	}
}

type flagValues struct {
	runID                 string
	version               string
	gatewayInImage        string
	gatewayOutImage       string
	fakeInternalImage     string
	timeout               time.Duration
	dcADMZKubeconfig      string
	dcADMZContext         string
	dcAInternalKubeconfig string
	dcAInternalContext    string
	dcAGatewayInTarget    string
	dcBDMZKubeconfig      string
	dcBDMZContext         string
	dcBInternalKubeconfig string
	dcBInternalContext    string
	dcBGatewayInTarget    string
}

func bindFlags(flags *flag.FlagSet) *flagValues {
	values := &flagValues{}
	flags.StringVar(&values.runID, "run-id", "", "bounded lower-kebab-case run identifier")
	flags.StringVar(&values.version, "version", "dev", "source version label")
	flags.StringVar(&values.gatewayInImage, "gateway-in-image", "", "gateway-in image reference")
	flags.StringVar(&values.gatewayOutImage, "gateway-out-image", "", "gateway-out image reference")
	flags.StringVar(&values.fakeInternalImage, "fake-internal-image", "", "fake internal image reference")
	flags.DurationVar(&values.timeout, "timeout", 3*time.Minute, "overall bounded command timeout")
	flags.StringVar(&values.dcADMZKubeconfig, "dc-a-dmz-kubeconfig", "", "explicit dc-a DMZ kubeconfig")
	flags.StringVar(&values.dcADMZContext, "dc-a-dmz-context", "dc-a-dmz", "dc-a DMZ context")
	flags.StringVar(
		&values.dcAInternalKubeconfig,
		"dc-a-internal-kubeconfig",
		"",
		"explicit dc-a internal kubeconfig",
	)
	flags.StringVar(
		&values.dcAInternalContext,
		"dc-a-internal-context",
		"dc-a-internal",
		"dc-a internal context",
	)
	flags.StringVar(
		&values.dcAGatewayInTarget,
		"dc-a-gateway-in-target",
		"",
		"fixed dc-a DMZ gateway-in target on port 30443",
	)
	flags.StringVar(&values.dcBDMZKubeconfig, "dc-b-dmz-kubeconfig", "", "explicit dc-b DMZ kubeconfig")
	flags.StringVar(&values.dcBDMZContext, "dc-b-dmz-context", "dc-b-dmz", "dc-b DMZ context")
	flags.StringVar(
		&values.dcBInternalKubeconfig,
		"dc-b-internal-kubeconfig",
		"",
		"explicit dc-b internal kubeconfig",
	)
	flags.StringVar(
		&values.dcBInternalContext,
		"dc-b-internal-context",
		"dc-b-internal",
		"dc-b internal context",
	)
	flags.StringVar(
		&values.dcBGatewayInTarget,
		"dc-b-gateway-in-target",
		"",
		"fixed dc-b DMZ gateway-in target on port 30443",
	)

	return values
}
