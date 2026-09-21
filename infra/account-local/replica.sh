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
 if [[ -n "$(find "$PGDATA" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "Incomplete replica data; preserve it and explicitly reset disposable data." >&2; exit 1
 fi
 pg_basebackup --dbname="host=$PRIMARY_HOST user=replicator application_name=account_sync" --pgdata="$PGDATA" --write-recovery-conf --wal-method=stream --slot=account_sync
fi
exec postgres -D "$PGDATA"
