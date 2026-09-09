#!/usr/bin/env bash
set -euo pipefail
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly TEST_PROJECT="marketmesh-mm14-test-$$"
compose() { docker compose --project-name "${TEST_PROJECT}" --file "${COMPOSE_FILE}" "$@"; }
cleanup() { compose down --volumes --remove-orphans --rmi local >/dev/null 2>&1 || true; }
trap cleanup EXIT
compose up --detach --wait --wait-timeout 120 postgres redis
compose build auth-session-integration
compose run --rm auth-session-integration
