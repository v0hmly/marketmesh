#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ENV_FILE="${REPOSITORY_ROOT}/infra/compose/.env"
readonly BASE_COMPOSE_FILE="${REPOSITORY_ROOT}/infra/compose/compose.yml"
readonly TEST_COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly TEST_PROJECT="marketmesh-mm20-test-$$"

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
  redis-edge \
  redis-auth
compose build redis-edge-integration redis-auth-integration
compose run --rm --no-deps redis-edge-integration
compose run --rm --no-deps redis-auth-integration
