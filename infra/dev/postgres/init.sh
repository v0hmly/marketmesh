#!/usr/bin/env bash
set -euo pipefail
psql --username "$POSTGRES_USER" --dbname postgres --set=ON_ERROR_STOP=1 --file=/run/shared-init.sql >/dev/null
# Files has one initial migration; Auth/User use the existing checksum ledger
# in account-local provision after the synchronous standby becomes healthy.
psql --username "$POSTGRES_USER" --dbname files --set=ON_ERROR_STOP=1 --file=/migrations/files/000001_files.up.sql >/dev/null
psql --username "$POSTGRES_USER" --dbname files --set=ON_ERROR_STOP=1 <<'SQL' >/dev/null
REVOKE ALL ON SCHEMA files FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA files FROM PUBLIC;
GRANT USAGE ON SCHEMA files TO files_rw, files_worker, files_ro;
GRANT SELECT, INSERT, UPDATE ON files.uploads TO files_rw;
GRANT SELECT, UPDATE ON files.uploads TO files_worker;
GRANT SELECT, INSERT ON files.audit TO files_rw, files_worker;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA files TO files_rw, files_worker;
GRANT SELECT ON files.uploads, files.audit TO files_ro;
SQL
cat > "$PGDATA/pg_hba.conf" <<'HBA'
local all all trust
hostssl replication replicator all scram-sha-256
hostssl auth auth_rw,auth_ro all scram-sha-256
hostssl user user_rw,user_ro all scram-sha-256
hostssl files files_rw,files_worker,files_ro all scram-sha-256
hostssl analytics rybbit all scram-sha-256
hostssl all fixture_admin all scram-sha-256
host all all all reject
HBA
psql --username "$POSTGRES_USER" --dbname postgres --set=ON_ERROR_STOP=1 <<'SQL' >/dev/null
SELECT pg_create_physical_replication_slot('marketmesh_sync');
ALTER SYSTEM SET synchronous_commit = 'remote_apply';
ALTER SYSTEM SET synchronous_standby_names = 'FIRST 1 (marketmesh_sync)';
SQL
