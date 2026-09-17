#!/bin/sh
set -eu
set -a
. /secrets/env
set +a
if [ "$1" = gateway-in ]; then
 export SERVICE_INSTANCE_ID="account-local-gateway-in-${HOSTNAME:?}"
fi
exec "$@"
