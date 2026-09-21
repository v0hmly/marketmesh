#!/usr/bin/env bash
set -euo pipefail
printf '\nhost replication replicator all scram-sha-256\n' >> "$PGDATA/pg_hba.conf"
psql --username "$POSTGRES_USER" --dbname postgres --set=ON_ERROR_STOP=1 \
 --set=auth_rw="$AUTH_RW_PASSWORD" --set=auth_ro="$AUTH_RO_PASSWORD" \
 --set=user_rw="$USER_RW_PASSWORD" --set=user_ro="$USER_RO_PASSWORD" --set=repl="$REPLICATOR_PASSWORD" <<'SQL'
CREATE ROLE auth_rw LOGIN PASSWORD :'auth_rw' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE auth_ro LOGIN PASSWORD :'auth_ro' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE user_rw LOGIN PASSWORD :'user_rw' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE user_ro LOGIN PASSWORD :'user_ro' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE replicator LOGIN REPLICATION PASSWORD :'repl' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
ALTER ROLE auth_ro SET default_transaction_read_only = on;
ALTER ROLE user_ro SET default_transaction_read_only = on;
CREATE DATABASE auth;
CREATE DATABASE "user";
REVOKE ALL ON DATABASE postgres, template1, auth, "user" FROM PUBLIC;
GRANT CONNECT ON DATABASE auth TO auth_rw, auth_ro;
GRANT CONNECT ON DATABASE "user" TO user_rw, user_ro;
\connect auth
REVOKE ALL ON SCHEMA public FROM PUBLIC;
\connect user
REVOKE ALL ON SCHEMA public FROM PUBLIC;
SELECT pg_create_physical_replication_slot('account_sync');
-- Enable only after initialization, before the final server starts. The replica
-- application_name is fixed in replica.sh; remote_apply guarantees RO visibility.
ALTER SYSTEM SET synchronous_commit = 'remote_apply';
ALTER SYSTEM SET synchronous_standby_names = 'FIRST 1 (account_sync)';
SQL
