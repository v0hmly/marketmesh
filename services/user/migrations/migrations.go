// Package migrations embeds User-owned schema migrations for controlled tooling.
package migrations

import _ "embed"

var (
	//go:embed 000001_profiles.up.sql
	ProfilesUp string
	//go:embed 000001_profiles.down.sql
	ProfilesDown string
)
