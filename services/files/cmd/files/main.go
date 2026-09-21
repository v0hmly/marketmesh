package main

import (
	"github.com/v0hmly/marketmesh/services/files/internal/app"
	"os"
)

func main() {
	if app.RunFiles() != nil {
		os.Stderr.WriteString("files: startup or runtime failure\n")
		os.Exit(1)
	}
}
