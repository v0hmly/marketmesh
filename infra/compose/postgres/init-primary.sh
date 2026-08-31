#!/usr/bin/env bash

set -euo pipefail

required_variables=(
  APP_RW_PASSWORD
  APP_RO_PASSWORD
  REPLICATOR_PASSWORD
)

for variable_name in "${required_variables[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'Переменная %s обязательна для инициализации PostgreSQL.\n' "${variable_name}" >&2
    exit 1
  fi
done

printf '\n# MarketMesh physical streaming replication\nhost replication replicator all scram-sha-256\n' \
  >>"${PGDATA}/pg_hba.conf"

psql \
  --username "${POSTGRES_USER}" \
  --dbname "${POSTGRES_DB}" \
  --set=ON_ERROR_STOP=1 \
  --set=app_rw_password="${APP_RW_PASSWORD}" \
  --set=app_ro_password="${APP_RO_PASSWORD}" \
  --set=replicator_password="${REPLICATOR_PASSWORD}" <<'SQL'
CREATE ROLE app_rw
  LOGIN
  PASSWORD :'app_rw_password'
  NOSUPERUSER
  NOCREATEDB
  NOCREATEROLE
  NOREPLICATION
  NOBYPASSRLS;

CREATE ROLE app_ro
  LOGIN
  PASSWORD :'app_ro_password'
  NOSUPERUSER
  NOCREATEDB
  NOCREATEROLE
  NOREPLICATION
  NOBYPASSRLS;

CREATE ROLE replicator
  LOGIN
  REPLICATION
  PASSWORD :'replicator_password'
  NOSUPERUSER
  NOCREATEDB
  NOCREATEROLE
  NOBYPASSRLS;

ALTER ROLE app_ro SET default_transaction_read_only = on;

REVOKE CREATE, TEMPORARY ON DATABASE marketmesh FROM PUBLIC;
GRANT CONNECT ON DATABASE marketmesh TO app_rw, app_ro;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO app_rw, app_ro;
GRANT CREATE ON SCHEMA public TO app_rw;

SET ROLE app_rw;
CREATE TABLE public.infra_smoke (
  marker text PRIMARY KEY,
  created_at timestamp with time zone NOT NULL DEFAULT clock_timestamp()
);
RESET ROLE;

GRANT SELECT ON TABLE public.infra_smoke TO app_ro;
ALTER DEFAULT PRIVILEGES FOR ROLE app_rw IN SCHEMA public
  GRANT SELECT ON TABLES TO app_ro;

SELECT pg_create_physical_replication_slot('postgres_sync_slot');
SELECT pg_create_physical_replication_slot('postgres_async_slot');

ALTER SYSTEM SET synchronous_commit = 'remote_apply';
ALTER SYSTEM SET synchronous_standby_names = 'FIRST 1 (postgres_sync)';
SQL

printf 'Роли приложений, слоты репликации и synchronous_commit настроены.\n'
