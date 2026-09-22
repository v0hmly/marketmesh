// Package migrations embeds Files-owned schema changes for controlled tooling.
package migrations

import _ "embed"

//go:embed 000001_files.up.sql
var Up string

//go:embed 000001_files.down.sql
var Down string
