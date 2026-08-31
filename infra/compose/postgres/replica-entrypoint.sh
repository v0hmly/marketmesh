#!/usr/bin/env bash

set -euo pipefail

readonly REPLICA_SCRIPT="/usr/local/bin/replica-entrypoint"
readonly POSTGRES_PASSFILE="/var/lib/postgresql/.pgpass"

required_variables=(
  PGDATA
  PRIMARY_HOST
  REPLICA_APPLICATION_NAME
  REPLICATION_SLOT
  REPLICATOR_PASSWORD
)

for variable_name in "${required_variables[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'Переменная %s обязательна для запуска PostgreSQL replica.\n' "${variable_name}" >&2
    exit 1
  fi
done

if [[ "$(id -u)" -eq 0 ]]; then
  mkdir -p "${PGDATA}"
  chown -R postgres:postgres /var/lib/postgresql
  exec gosu postgres "${REPLICA_SCRIPT}" "$@"
fi

umask 077
printf '%s:%s:%s:%s:%s\n' \
  "${PRIMARY_HOST}" \
  "5432" \
  "replication" \
  "replicator" \
  "${REPLICATOR_PASSWORD}" >"${POSTGRES_PASSFILE}"

if [[ ! -s "${PGDATA}/PG_VERSION" ]]; then
  if [[ -n "$(find "${PGDATA}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    printf 'Каталог PGDATA не пуст и не содержит PG_VERSION: %s\n' "${PGDATA}" >&2
    exit 1
  fi

  printf 'Ожидание primary %s для инициализации %s...\n' \
    "${PRIMARY_HOST}" "${REPLICA_APPLICATION_NAME}"
  until pg_isready --quiet --host "${PRIMARY_HOST}" --port 5432; do
    sleep 2
  done

  pg_basebackup \
    --dbname="host=${PRIMARY_HOST} port=5432 user=replicator passfile=${POSTGRES_PASSFILE} application_name=${REPLICA_APPLICATION_NAME}" \
    --pgdata="${PGDATA}" \
    --format=plain \
    --wal-method=stream \
    --write-recovery-conf \
    --slot="${REPLICATION_SLOT}" \
    --progress

  chmod 0700 "${PGDATA}"
fi

exec "$@"
