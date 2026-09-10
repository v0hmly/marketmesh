#!/usr/bin/env bash
set -euo pipefail
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly COMPOSE_FILE="${SCRIPT_DIR}/compose.yml"
readonly TEST_PROJECT="marketmesh-user-registration-test-$$"
# A caller may supply a locally built, verified offline image; no source or secret bind mounts are used.
readonly PROVIDED_IMAGE="${REGISTRATION_TEST_IMAGE:-}"
export REGISTRATION_TEST_IMAGE="${REGISTRATION_TEST_IMAGE:-${TEST_PROJECT}-integration}"
compose(){ docker compose --project-name "$TEST_PROJECT" --file "$COMPOSE_FILE" "$@"; }
cleanup(){
 compose down --volumes --remove-orphans >/dev/null 2>&1 || true
 if [[ -z "$PROVIDED_IMAGE" ]]; then docker image rm "$REGISTRATION_TEST_IMAGE" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
if [[ -z "$PROVIDED_IMAGE" ]]; then compose build user-registration-integration; fi
compose run --rm --no-deps fixture-generator
compose up --detach --wait --wait-timeout 120 postgres postgres-ro user-postgres user-postgres-ro nats
compose run --rm --no-deps user-registration-integration
