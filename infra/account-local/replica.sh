#!/usr/bin/env bash
set -euo pipefail
if [[ $(id -u) == 0 ]]; then
 install -d -o postgres -g postgres -m 0700 "$PGDATA"
 exec gosu postgres bash /replica.sh
fi
umask 077
export PGPASSFILE=/var/lib/postgresql/.pgpass
printf '%s:5432:*:replicator:%s\n' "$PRIMARY_HOST" "$REPLICATOR_PASSWORD" > "$PGPASSFILE"
if [[ ! -s "$PGDATA/PG_VERSION" ]]; then
 pg_basebackup --dbname="host=$PRIMARY_HOST user=replicator" --pgdata="$PGDATA" --write-recovery-conf --wal-method=stream
fi
exec postgres -D "$PGDATA"
