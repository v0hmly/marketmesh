#!/usr/bin/env bash

set -euo pipefail

readonly PRIMARY_HOST="postgres-rw"
readonly READ_ONLY_HOST="postgres-ro"
readonly DATABASE_NAME="${PGDATABASE:-marketmesh}"

required_variables=(
  POSTGRES_ADMIN_PASSWORD
  POSTGRES_APP_RW_PASSWORD
  POSTGRES_APP_RO_PASSWORD
)

for variable_name in "${required_variables[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'Переменная %s обязательна для smoke test PostgreSQL.\n' "${variable_name}" >&2
    exit 1
  fi
done

psql_value() {
  local host="$1"
  local user="$2"
  local password="$3"
  local query="$4"

  PGPASSWORD="${password}" psql \
    --host "${host}" \
    --username "${user}" \
    --dbname "${DATABASE_NAME}" \
    --no-psqlrc \
    --tuples-only \
    --no-align \
    --set=ON_ERROR_STOP=1 \
    --command "${query}"
}

wait_for_replication() {
  local attempt
  local replication_ready

  for attempt in $(seq 1 60); do
    replication_ready="$(psql_value \
      "${PRIMARY_HOST}" \
      postgres \
      "${POSTGRES_ADMIN_PASSWORD}" \
      "SELECT
         count(*) FILTER (
           WHERE application_name = 'postgres_sync'
             AND state = 'streaming'
             AND sync_state = 'sync'
         ) = 1
         AND count(*) FILTER (
           WHERE application_name = 'postgres_async'
             AND state = 'streaming'
             AND sync_state = 'async'
         ) = 1
       FROM pg_stat_replication;")"

    if [[ "${replication_ready}" == "t" ]]; then
      return
    fi

    sleep 2
  done

  printf 'PostgreSQL replicas не перешли в ожидаемые sync/async состояния.\n' >&2
  psql_value \
    "${PRIMARY_HOST}" \
    postgres \
    "${POSTGRES_ADMIN_PASSWORD}" \
    "SELECT application_name, state, sync_state FROM pg_stat_replication ORDER BY application_name;" >&2
  return 1
}

assert_equal() {
  local expected="$1"
  local actual="$2"
  local description="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    printf '%s: ожидалось %q, получено %q.\n' "${description}" "${expected}" "${actual}" >&2
    return 1
  fi
}

write_persistence_marker() {
  local marker="$1"

  PGPASSWORD="${POSTGRES_APP_RW_PASSWORD}" psql \
    --host "${PRIMARY_HOST}" \
    --username app_rw \
    --dbname "${DATABASE_NAME}" \
    --no-psqlrc \
    --quiet \
    --set=ON_ERROR_STOP=1 \
    --set=marker="${marker}" >/dev/null <<'SQL'
INSERT INTO public.infra_smoke (marker) VALUES (:'marker');
SQL
}

read_persistence_marker() {
  local marker="$1"

  PGPASSWORD="${POSTGRES_APP_RW_PASSWORD}" psql \
    --host "${PRIMARY_HOST}" \
    --username app_rw \
    --dbname "${DATABASE_NAME}" \
    --no-psqlrc \
    --quiet \
    --tuples-only \
    --no-align \
    --set=ON_ERROR_STOP=1 \
    --set=marker="${marker}" <<'SQL'
SELECT marker FROM public.infra_smoke WHERE marker = :'marker';
SQL
}

case "${1:-smoke}" in
  read-marker)
    read_persistence_marker "${2:?marker is required}"
    exit
    ;;
  smoke)
    ;;
  write-marker)
    write_persistence_marker "${2:?marker is required}"
    exit
    ;;
  *)
    printf 'Неизвестный режим PostgreSQL check: %s\n' "$1" >&2
    exit 2
    ;;
esac

wait_for_replication

assert_equal \
  remote_apply \
  "$(psql_value \
    "${PRIMARY_HOST}" \
    app_rw \
    "${POSTGRES_APP_RW_PASSWORD}" \
    'SHOW synchronous_commit;')" \
  'synchronous_commit primary'

assert_equal \
  on \
  "$(psql_value \
    "${READ_ONLY_HOST}" \
    app_ro \
    "${POSTGRES_APP_RO_PASSWORD}" \
    'SHOW default_transaction_read_only;')" \
  'default_transaction_read_only app_ro'

role_state="$(psql_value \
  "${PRIMARY_HOST}" \
  postgres \
  "${POSTGRES_ADMIN_PASSWORD}" \
  "SELECT string_agg(rolname || ':' || rolsuper || ':' || rolreplication, ',' ORDER BY rolname)
   FROM pg_roles
   WHERE rolname IN ('app_rw', 'app_ro', 'replicator');")"
assert_equal \
  'app_ro:false:false,app_rw:false:false,replicator:false:true' \
  "${role_state}" \
  'атрибуты ролей PostgreSQL'

readonly MARKER="mm9-$(date +%s)-$$"
psql_value \
  "${PRIMARY_HOST}" \
  app_rw \
  "${POSTGRES_APP_RW_PASSWORD}" \
  "INSERT INTO public.infra_smoke (marker) VALUES ('${MARKER}');" >/dev/null

assert_equal \
  "${MARKER}" \
  "$(psql_value \
    "${READ_ONLY_HOST}" \
    app_ro \
    "${POSTGRES_APP_RO_PASSWORD}" \
    "SELECT marker FROM public.infra_smoke WHERE marker = '${MARKER}';")" \
  'read-after-write через synchronous replica'

if psql_value \
  "${READ_ONLY_HOST}" \
  app_ro \
  "${POSTGRES_APP_RO_PASSWORD}" \
  "INSERT INTO public.infra_smoke (marker) VALUES ('forbidden-${MARKER}');" \
  >/dev/null 2>&1; then
  printf 'app_ro неожиданно выполнил запись через RO endpoint.\n' >&2
  exit 1
fi

printf 'PostgreSQL smoke test пройден: sync/async, роли, RW, RO и read-after-write проверены.\n'
