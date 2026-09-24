#!/usr/bin/env bash
set -euo pipefail
install -d -o postgres -g postgres -m 0700 /var/lib/postgresql/tls
install -o postgres -g postgres -m 0600 /pki/server.key /var/lib/postgresql/tls/server.key
install -o postgres -g postgres -m 0600 /pki/server.crt /var/lib/postgresql/tls/server.crt
exec docker-entrypoint.sh postgres \
  -c hba_file=/scripts/pg_hba.conf -c ssl=on -c ssl_min_protocol_version=TLSv1.3 \
  -c ssl_cert_file=/var/lib/postgresql/tls/server.crt \
  -c ssl_key_file=/var/lib/postgresql/tls/server.key \
  -c wal_level=replica -c max_wal_senders=4
