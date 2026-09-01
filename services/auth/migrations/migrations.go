// Package migrations embeds versioned Auth PostgreSQL migrations for controlled tooling and tests.
package migrations

import _ "embed"

var (
	// CredentialsUp creates the isolated Auth schema and credentials table.
	//go:embed 000001_credentials.up.sql
	CredentialsUp string

	// CredentialsDown removes the credentials table and its schema.
	//go:embed 000001_credentials.down.sql
	CredentialsDown string
)
