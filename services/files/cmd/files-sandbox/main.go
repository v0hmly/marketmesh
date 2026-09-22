package main

import (
	"github.com/v0hmly/marketmesh/services/files/internal/app"
	"os"
)

func main() {
	if app.RunSandbox() != nil {
		os.Stderr.WriteString("files sandbox stopped\n")
		os.Exit(1)
	}
}
