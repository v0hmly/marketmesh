#!/usr/bin/env bash
set -euo pipefail
# Persistent start gate is written before a fault. A returning old primary can
# never start automatically while the surviving node owns the write role.
while true; do
  role=$(cat /config/role) || exit 78
  case "$role" in
    fenced)
      cat /proc/sys/kernel/random/boot_id > /data/fenced-boot-id
      sleep 1
      ;;
    primary|replica) break ;;
    *) exit 78 ;;
  esac
done
rm -f /data/fenced-boot-id
install -d -o postgres -g postgres -m 0700 /data/tls /data/pg
for name in pg-primary.crt pg-primary.key ca.crt; do
  install -o postgres -g postgres -m 0600 "/config/pki/$name" "/data/tls/$name"
done
export PGDATA=/data/pg
export PGPASSFILE=/data/tls/replication.pgpass
export PGSSLMODE=verify-full PGSSLROOTCERT=/data/tls/ca.crt
export PGAPPNAME=${PGAPPNAME:-files_ro}
install -o postgres -g postgres -m 0600 /config/replication.pgpass "$PGPASSFILE"
if [[ $role == replica && ! -f "$PGDATA/PG_VERSION" ]]; then
  gosu postgres pg_basebackup -h pg-primary -p 5432 -U files_replicator -D "$PGDATA" -R -X stream
fi
exec docker-entrypoint.sh postgres -p "${PGPORT:-5432}" \
 -c ssl=on -c ssl_min_protocol_version=TLSv1.3 \
 -c ssl_cert_file=/data/tls/pg-primary.crt -c ssl_key_file=/data/tls/pg-primary.key \
 -c wal_level=replica -c wal_keep_size=256MB -c max_wal_senders=6 -c synchronous_commit=local \
 -c 'synchronous_standby_names=FIRST 1 (files_ro)'
