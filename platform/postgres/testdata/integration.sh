#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ENV_FILE="${REPOSITORY_ROOT}/infra/compose/.env"
readonly BASE_COMPOSE_FILE="${REPOSITORY_ROOT}/infra/compose/compose.yml"
readonly TEST_COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly TEST_PROJECT="marketmesh-mm18-test-$$"
readonly TEST_NETWORK="${TEST_PROJECT}-internal"

export MM18_POSTGRES_TEST_NETWORK="${TEST_NETWORK}"

compose() {
  docker compose \
    --project-name "${TEST_PROJECT}" \
    --env-file "${ENV_FILE}" \
    --file "${BASE_COMPOSE_FILE}" \
    --file "${TEST_COMPOSE_FILE}" \
    "$@"
}

cleanup() {
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}

"${REPOSITORY_ROOT}/infra/compose/scripts/local.sh" env
trap cleanup EXIT

compose up \
  --detach \
  --wait \
  --wait-timeout 240 \
  postgres-primary \
  postgres-sync
compose build postgres-integration
compose run --rm --no-deps postgres-integration
