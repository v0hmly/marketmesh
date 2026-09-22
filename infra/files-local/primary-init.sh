#!/usr/bin/env bash
set -euo pipefail
# Generated SQL contains only random fixture credentials and is not printed.
psql -v ON_ERROR_STOP=1 --username postgres --dbname postgres --file /run/files-init.sql >/dev/null
psql -v ON_ERROR_STOP=1 --username postgres --dbname files --file /migrations/000001_files.up.sql >/dev/null
psql -v ON_ERROR_STOP=1 --username postgres --dbname files <<'SQL' >/dev/null
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
hostssl replication files_replicator all scram-sha-256
hostssl files files_rw all scram-sha-256
hostssl files files_worker all scram-sha-256
hostssl files files_ro all scram-sha-256
hostssl all postgres all scram-sha-256
host all all all reject
HBA
