#!/usr/bin/env bash
set -euo pipefail
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../../../../../../../.." && pwd)"
readonly TEST_PROJECT="marketmesh-mm90-security-$$"
compose() { docker compose --project-name "${TEST_PROJECT}" --file "${SCRIPT_DIR}/compose.yml" "$@"; }
cleanup() { compose down --volumes --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT
compose up --detach --wait --wait-timeout 90
test_endpoint="$(compose port postgres 5432)"
if [[ ! "${test_endpoint}" =~ ^127\.0\.0\.1:[1-9][0-9]+$ ]]; then
  printf 'PostgreSQL test endpoint unavailable: %q\n' "${test_endpoint}" >&2
  exit 1
fi
cd "${REPO_ROOT}"
export GOWORK="${REPO_ROOT}/backend/go.work"
AUTH_SECURITY_TEST_DSN="postgres://auth_test:auth_test_password@${test_endpoint}/auth_security_test?sslmode=disable" \
  go test -race -tags=integration ./backend/services/auth/internal/adapter/out/postgressecurity -run TestSecurityLifecycle -count=1 -v
