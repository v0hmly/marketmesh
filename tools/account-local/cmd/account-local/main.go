// Command account-local provides disposable local account fixtures, never production configuration.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/v0hmly/marketmesh/tools/account-local/internal/fixture"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	if len(os.Args) < 2 {
		err = fmt.Errorf("command required")
	} else {
		switch os.Args[1] {
		case "generate":
			err = fixture.Generate(os.Getenv("FIXTURE_ROOT"), os.Getenv("ACCOUNT_LOCAL_PORT"))
		case "ready":
			err = fixture.Ready(ctx)
		case "probe":
			err = fixture.Probe(ctx)
		case "provision":
			err = fixture.Provision(ctx)
		case "frontdoor":
			err = fixture.Serve(ctx)
		default:
			err = fmt.Errorf("unknown command")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "account-local:", err)
		os.Exit(1)
	}
}
