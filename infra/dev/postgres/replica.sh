#!/usr/bin/env bash
set -euo pipefail
install -d -o postgres -g postgres -m 0700 /var/lib/postgresql/tls "$PGDATA"
install -o postgres -g postgres -m 0600 /pki/server.key /var/lib/postgresql/tls/server.key
install -o postgres -g postgres -m 0600 /pki/server.crt /var/lib/postgresql/tls/server.crt
install -o postgres -g postgres -m 0600 /run/replication.pgpass /var/lib/postgresql/tls/replication.pgpass
install -o postgres -g postgres -m 0644 /pki/ca.crt /var/lib/postgresql/tls/ca.crt
export PGPASSFILE=/var/lib/postgresql/tls/replication.pgpass
if [[ ! -s "$PGDATA/PG_VERSION" ]]; then
  if [[ -n "$(find "$PGDATA" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo 'Incomplete replica: explicit dev reset required.' >&2; exit 1
  fi
  gosu postgres pg_basebackup --dbname='host=pg-primary user=replicator application_name=marketmesh_sync sslmode=verify-full sslrootcert=/var/lib/postgresql/tls/ca.crt' \
    --pgdata="$PGDATA" --write-recovery-conf --wal-method=stream --slot=marketmesh_sync
fi
exec gosu postgres postgres -D "$PGDATA" \
  -c hba_file=/scripts/pg_hba.conf -c ssl=on -c ssl_min_protocol_version=TLSv1.3 \
  -c ssl_cert_file=/var/lib/postgresql/tls/server.crt -c ssl_key_file=/var/lib/postgresql/tls/server.key
