// Package migrations embeds versioned Auth PostgreSQL migrations for controlled tooling and tests.
package migrations

import _ "embed"

var (
	// RegistrationLoginUp binds first login to its registration browser.
	//go:embed 000005_registration_login.up.sql
	RegistrationLoginUp string

	// RegistrationLoginDown drops pending browser bindings without changing accounts.
	//go:embed 000005_registration_login.down.sql
	RegistrationLoginDown string

	// SecurityUp adds email challenges and the encrypted transactional mail queue.
	//go:embed 000004_security.up.sql
	SecurityUp string

	// SecurityDown removes email security after traffic and sessions are stopped.
	//go:embed 000004_security.down.sql
	SecurityDown string

	// RegistrationOutboxUp creates the durable registration publication queue.
	//go:embed 000003_registration_outbox.up.sql
	RegistrationOutboxUp string
	// RegistrationOutboxDown removes the registration queue; pending events are lost.
	//go:embed 000003_registration_outbox.down.sql
	RegistrationOutboxDown string

	// CredentialsUp creates the isolated Auth schema and credentials table.
	//go:embed 000001_credentials.up.sql
	CredentialsUp string

	// CredentialsDown removes the credentials table and its schema.
	//go:embed 000001_credentials.down.sql
	CredentialsDown string

	// SessionsUp creates durable session families, refresh history, and revocation outbox.
	//go:embed 000002_sessions.up.sql
	SessionsUp string

	// SessionsDown removes session storage before the credentials migration is reverted.
	//go:embed 000002_sessions.down.sql
	SessionsDown string
)
