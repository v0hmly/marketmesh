#!/usr/bin/env bash
set -euo pipefail
readonly DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export RYBBIT_WORKSPACE="$DIR"
export RYBBIT_PROJECT="${RYBBIT_PROJECT:-marketmesh-rybbit-local}"
export RYBBIT_PORT="${RYBBIT_PORT:-8310}"
if [[ "${1:-}" == verify ]]; then
 export RYBBIT_PROJECT="marketmesh-rybbit-test-$(date +%s)-$$"
 export RYBBIT_PORT="${RYBBIT_TEST_PORT:-18310}"
fi
[[ "$RYBBIT_PROJECT" =~ ^marketmesh-rybbit-[a-z0-9][a-z0-9-]{0,39}$ ]] || { echo 'Недопустимое имя Compose-проекта.' >&2; exit 1; }
for port in "$RYBBIT_PORT"; do
 [[ "$port" =~ ^[1-9][0-9]{3,4}$ ]] && (( 10#$port >= 1024 && 10#$port <= 65535 )) || { echo 'Недопустимый loopback-порт.' >&2; exit 1; }
done
export RYBBIT_STATE="$DIR/.state/$RYBBIT_PROJECT"
compose(){ docker compose --env-file "$RYBBIT_STATE/.env" --project-directory "$DIR" --project-name "$RYBBIT_PROJECT" --file "$DIR/compose.yml" "$@"; }
owned(){
 local kind id owner ids
 ids=$(docker ps -aq --filter "label=com.docker.compose.project=$RYBBIT_PROJECT")
 for id in $ids; do
  owner=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$id")
  [[ "$owner" == "$DIR" ]] || { echo 'Проект занят другим worktree.' >&2; return 1; }
 done
 for kind in volume network; do
  ids=$(docker "$kind" ls -q --filter "label=com.docker.compose.project=$RYBBIT_PROJECT")
  for id in $ids; do
   owner=$(docker "$kind" inspect --format '{{ index .Labels "marketmesh.rybbit.workspace" }}' "$id")
   [[ "$owner" == "$DIR" ]] || { echo 'Данные принадлежат другому worktree.' >&2; return 1; }
  done
 done
}
generate(){
 [[ ! -L "$DIR/.state" && ! -L "$RYBBIT_STATE" ]] || { echo 'Symlink state refused.' >&2; exit 1; }
 umask 077
 mkdir -p "$RYBBIT_STATE"
 python3 "$DIR/state.py"
}
up(){ generate; compose up -d --wait --wait-timeout 240; python3 "$DIR/bootstrap.py"; }
owned
case "${1:-}" in
 config) generate; compose config --quiet ;;
 up) up; printf 'Rybbit: http://localhost:%s; локальная учётная запись: %s/admin.json\n' "$RYBBIT_PORT" "$RYBBIT_STATE" ;;
 status) compose ps ;;
 logs) compose logs -f ;;
 down) generate; compose down ;;
 clean) generate; compose down --volumes; rm -rf -- "$RYBBIT_STATE" ;;
 verify)
  [[ ! -e "$RYBBIT_STATE" && -z "$(docker ps -aq --filter "label=com.docker.compose.project=$RYBBIT_PROJECT")" && -z "$(docker volume ls -q --filter "label=com.docker.compose.project=$RYBBIT_PROJECT")" ]] || { echo 'Нужен пустой тестовый проект.' >&2; exit 1; }
  trap 'owned && compose down --volumes' EXIT
  up
  python3 "$DIR/verify.py" send
  compose restart
  compose up -d --wait --wait-timeout 240
  python3 "$DIR/verify.py" check
  ;;
 *) echo 'usage: local.sh {config|up|status|logs|down|clean|verify}' >&2; exit 2 ;;
esac
