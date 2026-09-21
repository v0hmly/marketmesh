#!/usr/bin/env bash
set -euo pipefail
readonly DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export MAILPIT_WORKSPACE="$DIR"
export MAILPIT_PROJECT="${MAILPIT_PROJECT:-marketmesh-mailpit-local}"
export MAILPIT_UI_PORT="${MAILPIT_UI_PORT:-8025}"
export MAILPIT_SMTP_PORT="${MAILPIT_SMTP_PORT:-1025}"
if [[ "${1:-}" == verify ]]; then
 export MAILPIT_PROJECT="marketmesh-mailpit-test-$(date +%s)-$$"
 export MAILPIT_UI_PORT="${MAILPIT_TEST_UI_PORT:-18025}"
 export MAILPIT_SMTP_PORT="${MAILPIT_TEST_SMTP_PORT:-11025}"
fi
[[ "$MAILPIT_PROJECT" =~ ^marketmesh-mailpit-[a-z0-9][a-z0-9-]{0,39}$ ]] || { echo 'Недопустимое имя Compose-проекта.' >&2; exit 1; }
for port in "$MAILPIT_UI_PORT" "$MAILPIT_SMTP_PORT"; do
 [[ "$port" =~ ^[1-9][0-9]{3,4}$ ]] && (( 10#$port >= 1024 && 10#$port <= 65535 )) || { echo 'Недопустимый loopback-порт.' >&2; exit 1; }
done
compose(){ docker compose --project-directory "$DIR" --project-name "$MAILPIT_PROJECT" --file "$DIR/compose.yml" "$@"; }
owned(){
 local kind id owner ids
 ids=$(docker ps -aq --filter "label=com.docker.compose.project=$MAILPIT_PROJECT")
 for id in $ids; do
  owner=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$id")
  [[ "$owner" == "$DIR" ]] || { echo 'Проект занят другим worktree.' >&2; return 1; }
 done
 for kind in volume network; do
  ids=$(docker "$kind" ls -q --filter "label=com.docker.compose.project=$MAILPIT_PROJECT")
  for id in $ids; do
   owner=$(docker "$kind" inspect --format '{{ index .Labels "marketmesh.mailpit.workspace" }}' "$id")
   [[ "$owner" == "$DIR" ]] || { echo 'Данные принадлежат другому worktree.' >&2; return 1; }
  done
 done
}
up(){ compose up -d --wait --wait-timeout 90; }
owned
case "${1:-}" in
 config) compose config --quiet ;;
 up) up; printf 'Mailpit: http://localhost:%s; SMTP: localhost:%s (без TLS и авторизации)\n' "$MAILPIT_UI_PORT" "$MAILPIT_SMTP_PORT" ;;
 status) compose ps ;;
 logs) compose logs -f ;;
 down) compose down ;;
 clean) compose down --volumes ;;
 verify)
  [[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$MAILPIT_PROJECT")" && -z "$(docker volume ls -q --filter "label=com.docker.compose.project=$MAILPIT_PROJECT")" ]] || { echo 'Нужен пустой тестовый проект.' >&2; exit 1; }
  trap 'owned && compose down --volumes' EXIT
  compose config --quiet
  up
  python3 "$DIR/verify.py" send
  compose restart
  up
  python3 "$DIR/verify.py" check
  ;;
 *) echo 'usage: local.sh {config|up|status|logs|down|clean|verify}' >&2; exit 2 ;;
esac
