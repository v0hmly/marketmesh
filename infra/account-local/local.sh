#!/usr/bin/env bash
set -euo pipefail
umask 077
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
export GOWORK="$ROOT/backend/go.work"
if [[ "${1:-}" == test ]]; then
 export ACCOUNT_MAILPIT_PORT=0
 export ACCOUNT_LOCAL_PROJECT="marketmesh-account-test-$(date +%s)-$$"
fi
export ACCOUNT_LOCAL_PROJECT="${ACCOUNT_LOCAL_PROJECT:-marketmesh-account-local}"
export ACCOUNT_LOCAL_PORT="${ACCOUNT_LOCAL_PORT:-8443}"
[[ "$ACCOUNT_LOCAL_PROJECT" =~ ^marketmesh-account-[a-z0-9][a-z0-9-]{0,39}$ ]] || { echo 'Project must use marketmesh-account- prefix and safe lowercase suffix.' >&2; exit 1; }
[[ "$ACCOUNT_LOCAL_PORT" =~ ^[1-9][0-9]{3,4}$ ]] && (( 10#$ACCOUNT_LOCAL_PORT >= 1024 && 10#$ACCOUNT_LOCAL_PORT <= 65535 )) || { echo 'Invalid loopback port.' >&2; exit 1; }
readonly STATE_PARENT="$SCRIPT_DIR/.state"
export ACCOUNT_LOCAL_STATE="$STATE_PARENT/$ACCOUNT_LOCAL_PROJECT"
export ACCOUNT_LOCAL_WORKSPACE="$SCRIPT_DIR"
export ACCOUNT_LOCAL_IMAGE="${ACCOUNT_LOCAL_IMAGE:-$ACCOUNT_LOCAL_PROJECT:local}"
readonly RYBBIT_OVERRIDE="$ROOT/infra/rybbit/account.override.yml"
compose(){
 local files=(--file "$SCRIPT_DIR/compose.yml")
 if [[ "${ACCOUNT_RYBBIT_ENABLED:-false}" == true ]]; then files+=(--file "$RYBBIT_OVERRIDE"); fi
 docker compose --project-directory "$SCRIPT_DIR" --project-name "$ACCOUNT_LOCAL_PROJECT" "${files[@]}" "$@"; }
owned(){ [[ -f "$ACCOUNT_LOCAL_STATE/.owner" && "$(cat "$ACCOUNT_LOCAL_STATE/.owner")" == "$ACCOUNT_LOCAL_PROJECT|$SCRIPT_DIR" ]] || { echo 'State ownership marker missing or mismatched.' >&2; exit 1; }; }
check_paths(){ [[ ! -L "$STATE_PARENT" && ! -L "$ACCOUNT_LOCAL_STATE" ]] || { echo 'Symlink state refused.' >&2; exit 1; }; }
check_project(){
 local id directory ids kind
 ids=$(docker ps -aq --filter "label=com.docker.compose.project=$ACCOUNT_LOCAL_PROJECT")
 for id in $ids; do
  directory=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$id")
  [[ "$directory" == "$SCRIPT_DIR" ]] || { echo 'Compose project belongs to another workspace.' >&2; exit 1; }
 done
 for kind in volume network; do
  ids=$(docker "$kind" ls -q --filter "label=com.docker.compose.project=$ACCOUNT_LOCAL_PROJECT")
  for id in $ids; do
   directory=$(docker "$kind" inspect --format '{{ index .Labels "marketmesh.account.workspace" }}' "$id")
   [[ "$directory" == "$SCRIPT_DIR" ]] || { echo 'Persistent Compose resources belong to another workspace.' >&2; exit 1; }
  done
 done
}
# Does not parse Compose env_file: also works after interrupted fixture generation.
remove_resources(){
 check_project
 local ids id kind
 ids=$(docker ps -aq --filter "label=com.docker.compose.project=$ACCOUNT_LOCAL_PROJECT")
 for id in $ids; do docker rm -f "$id" >/dev/null; done
 for kind in network volume; do
  ids=$(docker "$kind" ls -q --filter "label=com.docker.compose.project=$ACCOUNT_LOCAL_PROJECT")
  for id in $ids; do docker "$kind" rm "$id" >/dev/null; done
 done
}
generate(){
 check_paths
 mkdir -p "$STATE_PARENT"; chmod 700 "$STATE_PARENT"
 if [[ -d "$ACCOUNT_LOCAL_STATE" ]]; then
  owned
 else
  mkdir "$ACCOUNT_LOCAL_STATE"
  printf '%s|%s\n' "$ACCOUNT_LOCAL_PROJECT" "$SCRIPT_DIR" > "$ACCOUNT_LOCAL_STATE/.owner"
 fi
 export FIXTURE_ROOT="$ACCOUNT_LOCAL_STATE"
 (cd "$ROOT/backend/tools/account-local" && go run ./cmd/account-local generate)
}
wait_ready(){
 (cd "$ROOT/backend/tools/account-local" && go run ./cmd/account-local ready)
}
up(){
 generate
 compose build auth
 compose up -d --wait --wait-timeout 150 postgres-primary postgres-replica
 compose up -d redis nats
 compose up -d --wait --wait-timeout 90 mailpit
 compose run --rm --no-deps provision
 compose up -d --wait --wait-timeout 120 auth
 compose up -d --wait --wait-timeout 120 user
 compose up -d --scale gateway-in=2 gateway-in frontdoor
 compose up -d --wait --wait-timeout 150 gateway-out
 wait_ready
 printf 'Account: https://localhost:%s (local CA: %s/browser/ca.pem)\n' "$ACCOUNT_LOCAL_PORT" "$ACCOUNT_LOCAL_STATE"
}
check_paths
check_project
case "${1:-}" in
 up) up ;;
 status) owned; compose ps ;;
 probe) owned; compose run --rm --no-deps provision account-local probe ;;
 down) owned; compose down --remove-orphans ;;
 reset) owned; remove_resources; rm -rf -- "$ACCOUNT_LOCAL_STATE" ;;
 test)
  [[ ! -e "$ACCOUNT_LOCAL_STATE" ]] || { echo 'Fresh test project required.' >&2; exit 1; }
  [[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$ACCOUNT_LOCAL_PROJECT")" && -z "$(docker volume ls -q --filter "label=com.docker.compose.project=$ACCOUNT_LOCAL_PROJECT")" ]] || { echo 'Fresh test resources required.' >&2; exit 1; }
  # Always retain scoped artifacts, but remove only this fresh test project containers/data.
  trap 'test_status=$?; trap - EXIT; remove_resources || test_status=1; exit "$test_status"' EXIT
  export ACCOUNT_USER_CONSUME_ENABLED=false
  up
  export ACCOUNT_E2E_RUN_ID="run-$(date +%s)-$$"
  export ACCOUNT_E2E_PHASE=pending
  compose build browser
  compose run --rm --no-deps browser
  compose run --rm --no-deps provision account-local database-check seed
  compose restart postgres-primary postgres-replica
  compose up -d --wait --wait-timeout 150 postgres-primary postgres-replica
  compose run --rm --no-deps provision account-local database-check check
  compose run --rm --no-deps provision account-local database-check clean
  export ACCOUNT_USER_CONSUME_ENABLED=true
  compose up -d --wait --wait-timeout 120 --force-recreate user
  export ACCOUNT_E2E_PHASE=core
  compose run --rm --no-deps browser
  ;;
 *) echo 'usage: local.sh {up|status|probe|down|reset|test}' >&2; exit 2 ;;
esac
