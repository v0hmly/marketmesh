// Package migrations embeds User-owned schema migrations for controlled tooling.
package migrations

import _ "embed"

var (
	//go:embed 000001_profiles.up.sql
	ProfilesUp string
	//go:embed 000001_profiles.down.sql
	ProfilesDown string
)

var (
	//go:embed 000002_registration_inbox.up.sql
	RegistrationInboxUp string
	//go:embed 000002_registration_inbox.down.sql
	RegistrationInboxDown string
)

var (
	//go:embed 000003_addresses.up.sql
	AddressesUp string
	//go:embed 000003_addresses.down.sql
	AddressesDown string
)
