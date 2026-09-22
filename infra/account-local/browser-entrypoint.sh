#!/bin/sh
set -eu
: "${PUBLIC_CA:?public CA path required}"
: "${ACCOUNT_E2E_RUN_ID:?unique run id required}"
test -r "$PUBLIC_CA"
# Chromium uses the legacy user NSS database when present. Never alter host trust.
mkdir -p "$HOME/.pki/nssdb"
certutil -N --empty-password -d "sql:$HOME/.pki/nssdb"
certutil -A -d "sql:$HOME/.pki/nssdb" -n marketmesh-local -t 'C,,' -i "$PUBLIC_CA"
if [ -n "${PUBLIC_FILES_CA:-}" ]; then
  test -r "$PUBLIC_FILES_CA"
  certutil -A -d "sql:$HOME/.pki/nssdb" -n marketmesh-files -t 'C,,' -i "$PUBLIC_FILES_CA"
fi
# Playwright route.fetch uses Node TLS, which needs the same public test CA.
export NODE_EXTRA_CA_CERTS="${PUBLIC_COMBINED_CA:-$PUBLIC_CA}"
exec pnpm exec playwright test --config playwright.real.config.ts "$@"
