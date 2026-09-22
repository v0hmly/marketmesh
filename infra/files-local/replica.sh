#!/usr/bin/env bash
set -euo pipefail
install -d -o postgres -g postgres -m 0700 /var/lib/postgresql/tls /var/lib/postgresql/18/replica
install -o postgres -g postgres -m 0600 /pki/server.key /var/lib/postgresql/tls/server.key
install -o postgres -g postgres -m 0600 /pki/server.crt /var/lib/postgresql/tls/server.crt
install -o postgres -g postgres -m 0600 /run/replication.pgpass /var/lib/postgresql/tls/replication.pgpass
install -o postgres -g postgres -m 0644 /pki/ca.crt /var/lib/postgresql/tls/ca.crt
export PGPASSFILE=/var/lib/postgresql/tls/replication.pgpass
export PGSSLMODE=verify-full PGSSLROOTCERT=/var/lib/postgresql/tls/ca.crt
export PGAPPNAME=files_ro
if [[ ! -f /var/lib/postgresql/18/replica/PG_VERSION ]]; then
  gosu postgres pg_basebackup -h pg-primary -U files_replicator -D /var/lib/postgresql/18/replica -R -X stream
fi
exec gosu postgres postgres -D /var/lib/postgresql/18/replica \
  -c ssl=on -c ssl_min_protocol_version=TLSv1.3 \
  -c ssl_cert_file=/var/lib/postgresql/tls/server.crt -c ssl_key_file=/var/lib/postgresql/tls/server.key
