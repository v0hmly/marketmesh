#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly COMPOSE_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${COMPOSE_DIR}/compose.yml"
readonly ENV_FILE="${COMPOSE_DIR}/.env"
readonly OBSERVABILITY_CHECK="${COMPOSE_DIR}/checks/observability-smoke.sh"
readonly -a OBSERVABILITY_SERVICES=(observability-init alloy-dmz alloy-internal tempo loki grafana observability-gateway)

usage() {
  printf 'usage: %s {clean|config|down|env|logs|observability-clean|observability-config|observability-down|observability-logs|observability-outage|observability-persistence|observability-ready|observability-smoke|observability-status|observability-up|observability-verify|persistence|psql-ro|psql-rw|ready|smoke|status|up|verify}\n' "$0" >&2
}

random_hex() {
  local bytes="$1"

  if ! command -v openssl >/dev/null 2>&1; then
    printf 'Для создания локальных секретов требуется openssl.\n' >&2
    return 1
  fi

  openssl rand -hex "${bytes}"
}

ensure_env() {
  local temporary_env

  if [[ -s "${ENV_FILE}" ]]; then
    return
  fi

  temporary_env="$(mktemp "${COMPOSE_DIR}/.env.tmp.XXXXXX")"
  trap 'rm -f -- "${temporary_env}"' RETURN
  chmod 0600 "${temporary_env}"

  {
    printf 'COMPOSE_PROJECT_NAME=marketmesh\n'
    printf 'POSTGRES_ADMIN_PASSWORD=%s\n' "$(random_hex 24)"
    printf 'POSTGRES_APP_RW_PASSWORD=%s\n' "$(random_hex 24)"
    printf 'POSTGRES_APP_RO_PASSWORD=%s\n' "$(random_hex 24)"
    printf 'POSTGRES_REPLICATOR_PASSWORD=%s\n' "$(random_hex 24)"
    printf 'REDIS_EDGE_PASSWORD=%s\n' "$(random_hex 24)"
    printf 'REDIS_AUTH_PASSWORD=%s\n' "$(random_hex 24)"
    printf 'SEAWEED_QUARANTINE_ACCESS_KEY=mmq%s\n' "$(random_hex 8)"
    printf 'SEAWEED_QUARANTINE_SECRET_KEY=%s\n' "$(random_hex 24)"
    printf 'SEAWEED_PUBLIC_ACCESS_KEY=mmp%s\n' "$(random_hex 8)"
    printf 'SEAWEED_PUBLIC_SECRET_KEY=%s\n' "$(random_hex 24)"
    printf 'SEAWEED_INTERNAL_ACCESS_KEY=mmi%s\n' "$(random_hex 8)"
    printf 'SEAWEED_INTERNAL_SECRET_KEY=%s\n' "$(random_hex 24)"
  } >"${temporary_env}"

  mv "${temporary_env}" "${ENV_FILE}"
  trap - RETURN
  printf 'Создан локальный файл %s с правами 0600.\n' "${ENV_FILE}"
}

compose() {
  docker compose \
    --env-file "${ENV_FILE}" \
    --file "${COMPOSE_FILE}" \
    "$@"
}

published_port() {
  local service="$1"
  local container_port="$2"
  local endpoint

  endpoint="$(compose port "${service}" "${container_port}")"
  [[ -n "${endpoint}" ]] || {
    printf 'Не найден опубликованный порт %s для %s.\n' "${container_port}" "${service}" >&2
    return 1
  }

  printf '%s\n' "${endpoint##*:}"
}

set_observability_endpoints() {
  OBSERVABILITY_ALLOY_HTTP="http://127.0.0.1:$(published_port observability-gateway 4318)"
  OBSERVABILITY_TEMPO_HTTP="http://127.0.0.1:$(published_port observability-gateway 3200)"
  OBSERVABILITY_LOKI_HTTP="http://127.0.0.1:$(published_port observability-gateway 3100)"
  OBSERVABILITY_GRAFANA_HTTP="http://127.0.0.1:$(published_port observability-gateway 3000)"
}

wait_for_http() {
  local name="$1"
  local url="$2"
  local attempt

  for attempt in {1..60}; do
    if curl --fail --silent --show-error --max-time 5 "${url}" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done

  printf '%s не стал доступен по %s.\n' "${name}" "${url}" >&2
  return 1
}

wait_for_observability_http() {
  set_observability_endpoints
  wait_for_http Tempo "${OBSERVABILITY_TEMPO_HTTP}/ready"
  wait_for_http Loki "${OBSERVABILITY_LOKI_HTTP}/ready"
  wait_for_http Grafana "${OBSERVABILITY_GRAFANA_HTTP}/api/health"
}

validate_observability_configs() {
  ensure_env
  compose config --quiet
  compose run --rm --no-deps --entrypoint /bin/alloy \
    alloy-internal validate /etc/alloy/config.alloy
  compose run --rm --no-deps --entrypoint /usr/bin/loki \
    loki -config.file=/etc/loki/loki.yml -verify-config
  compose run --rm --no-deps --entrypoint /tempo \
    tempo -config.file=/etc/tempo/tempo.yml -config.verify=true
  compose run --rm --no-deps --entrypoint haproxy \
    observability-gateway -c -f /usr/local/etc/haproxy/haproxy.cfg
  printf 'Конфигурации Compose, Alloy, Loki, Tempo и HAProxy корректны.\n'
}

observability_up() {
  validate_observability_configs
  compose up \
    --detach \
    --remove-orphans \
    --wait \
    --wait-timeout 240 \
    "${OBSERVABILITY_SERVICES[@]}"
  wait_for_observability_http
  printf 'Локальный observability-стек MarketMesh готов.\n'
}

observability_ready() {
  ensure_env
  compose up \
    --detach \
    --remove-orphans \
    --wait \
    --wait-timeout 240 \
    "${OBSERVABILITY_SERVICES[@]}" >/dev/null
  wait_for_observability_http
}

assert_grafana_provisioning() {
  local health
  local loki
  local tempo
  local dashboards

  health="$(curl --fail --silent --show-error --max-time 10 "${OBSERVABILITY_GRAFANA_HTTP}/api/health")"
  [[ "${health}" == *'"database": "ok"'* || "${health}" == *'"database":"ok"'* ]] || {
    printf 'Grafana не сообщает о готовой базе данных.\n' >&2
    return 1
  }

  loki="$(curl --fail --silent --show-error --max-time 10 "${OBSERVABILITY_GRAFANA_HTTP}/api/datasources/uid/loki")"
  tempo="$(curl --fail --silent --show-error --max-time 10 "${OBSERVABILITY_GRAFANA_HTTP}/api/datasources/uid/tempo")"
  dashboards="$(curl --fail --silent --show-error --max-time 10 "${OBSERVABILITY_GRAFANA_HTTP}/api/search?tag=marketmesh")"

  [[ "${loki}" == *'"uid":"loki"'* || "${loki}" == *'"uid": "loki"'* ]] || {
    printf 'Grafana не видит provisioned Loki data source.\n' >&2
    return 1
  }
  [[ "${tempo}" == *'"uid":"tempo"'* || "${tempo}" == *'"uid": "tempo"'* ]] || {
    printf 'Grafana не видит provisioned Tempo data source.\n' >&2
    return 1
  }
  [[ "${dashboards}" == *'marketmesh-services'* && "${dashboards}" == *'marketmesh-observability'* ]] || {
    printf 'Grafana не видит provisioned dashboards MarketMesh.\n' >&2
    return 1
  }
}

emit_observability_marker() {
  local marker="$1"

  "${OBSERVABILITY_CHECK}" emit \
    "${OBSERVABILITY_ALLOY_HTTP}" \
    "${OBSERVABILITY_TEMPO_HTTP}" \
    "${OBSERVABILITY_LOKI_HTTP}" \
    "${marker}"
}

assert_observability_marker() {
  local marker="$1"
  local trace_id="$2"
  local span_id="$3"

  "${OBSERVABILITY_CHECK}" verify \
    "${OBSERVABILITY_ALLOY_HTTP}" \
    "${OBSERVABILITY_TEMPO_HTTP}" \
    "${OBSERVABILITY_LOKI_HTTP}" \
    "${marker}" \
    "${trace_id}" \
    "${span_id}"
}

observability_smoke() {
  local marker
  local result
  local trace_id
  local span_id

  observability_ready
  set_observability_endpoints
  marker="mm17-smoke-$(date +%s)-$$"
  result="$(emit_observability_marker "${marker}")"
  IFS='|' read -r trace_id span_id <<<"${result}"
  assert_grafana_provisioning
  printf 'Trace %s и связанный log прошли через Alloy и доступны в Tempo, Loki и Grafana.\n' "${trace_id}"
}

observability_persistence() {
  local marker
  local result
  local trace_id
  local span_id

  observability_ready
  set_observability_endpoints
  marker="mm17-persistence-$(date +%s)-$$"
  result="$(emit_observability_marker "${marker}")"
  IFS='|' read -r trace_id span_id <<<"${result}"

  compose restart "${OBSERVABILITY_SERVICES[@]}" >/dev/null
  observability_ready
  assert_observability_marker "${marker}" "${trace_id}" "${span_id}"
  assert_grafana_provisioning
  printf 'Tempo, Loki и Grafana сохранили данные после обычного restart.\n'
}

assert_bounded_alloy_runtime() {
  local container_id
  local memory_limit
  local service

  [[ "$(grep -Ec '^[[:space:]]*queue_size[[:space:]]*=[[:space:]]*64$' "${COMPOSE_DIR}/alloy/config.alloy")" == 2 ]] || {
    printf 'Ожидались две bounded Alloy queues размером 64.\n' >&2
    return 1
  }
  [[ "$(grep -Ec '^[[:space:]]*max_elapsed_time[[:space:]]*=[[:space:]]*"30s"$' "${COMPOSE_DIR}/alloy/config.alloy")" == 2 ]] || {
    printf 'Ожидался ограниченный 30 секундами retry обоих Alloy exporters.\n' >&2
    return 1
  }

  for service in alloy-dmz alloy-internal; do
    container_id="$(compose ps --quiet "${service}")"
    [[ -n "${container_id}" ]] || {
      printf 'Не найден контейнер %s.\n' "${service}" >&2
      return 1
    }
    memory_limit="$(docker inspect --format '{{.HostConfig.Memory}}' "${container_id}")"
    [[ "${memory_limit}" == 167772160 ]] || {
      printf 'Memory limit %s не равен ожидаемым 160 MiB.\n' "${service}" >&2
      return 1
    }
  done
}

observability_outage() {
  local container_id
  local marker
  local service

  observability_ready
  assert_bounded_alloy_runtime
  compose stop tempo loki >/dev/null
  trap 'compose start tempo loki >/dev/null 2>&1 || true' RETURN

  for marker in {1..40}; do
    "${OBSERVABILITY_CHECK}" send \
      "${OBSERVABILITY_ALLOY_HTTP}" \
      "${OBSERVABILITY_TEMPO_HTTP}" \
      "${OBSERVABILITY_LOKI_HTTP}" \
      "mm17-outage-${marker}-$(date +%s)-$$" >/dev/null
  done
  sleep 35

  for service in alloy-dmz alloy-internal; do
    container_id="$(compose ps --quiet "${service}")"
    [[ "$(docker inspect --format '{{.State.Status}}:{{.State.OOMKilled}}' "${container_id}")" == "running:false" ]] || {
      printf '%s не пережил временную недоступность backends.\n' "${service}" >&2
      return 1
    }
  done

  compose start tempo loki >/dev/null
  trap - RETURN
  observability_ready
  observability_smoke
  printf 'Alloy сохранил работоспособность с bounded queues при временно недоступных Tempo и Loki.\n'
}

observability_down() {
  ensure_env
  compose stop "${OBSERVABILITY_SERVICES[@]}"
}

observability_clean() {
  local project_name
  local volume

  ensure_env
  project_name="${COMPOSE_PROJECT_NAME:-$(awk -F= '$1 == "COMPOSE_PROJECT_NAME" { print $2 }' "${ENV_FILE}")}"
  [[ -n "${project_name}" ]] || project_name="marketmesh"

  compose rm --stop --force "${OBSERVABILITY_SERVICES[@]}"
  for volume in \
    "${project_name}-tempo-data" \
    "${project_name}-loki-data" \
    "${project_name}-grafana-data"; do
    if docker volume inspect "${volume}" >/dev/null 2>&1; then
      docker volume rm "${volume}"
    fi
  done
  printf 'Контейнеры observability и их volumes полностью удалены.\n'
}

config() {
  ensure_env
  compose config --quiet
  printf 'Docker Compose конфигурация корректна.\n'
}

up() {
  config
  compose up \
    --detach \
    --remove-orphans \
    --wait \
    --wait-timeout 240
  printf 'Локальная инфраструктура MarketMesh готова.\n'
}

ready() {
  ensure_env
  compose up \
    --detach \
    --remove-orphans \
    --wait \
    --wait-timeout 240 >/dev/null
  printf 'Все постоянные контейнеры запущены и прошли health checks.\n'
}

smoke() {
  ready
  compose run --rm --no-deps postgres-check
  compose run --rm --no-deps dmz-probe
  observability_smoke
}

write_persistence_markers() {
  local marker="$1"

  compose run --rm --no-deps postgres-check write-marker "${marker}"

  compose exec -T redis-edge \
    sh -ceu \
    'REDISCLI_AUTH="${REDIS_PASSWORD}" redis-cli --no-auth-warning SET mm9:persistence "$1" >/dev/null' \
    sh "${marker}"
  compose exec -T redis-auth \
    sh -ceu \
    'REDISCLI_AUTH="${REDIS_PASSWORD}" redis-cli --no-auth-warning SET mm9:persistence "$1" >/dev/null' \
    sh "${marker}"

  for service in seaweed-quarantine seaweed-public seaweed-internal; do
    compose exec -T "${service}" \
      sh -ceu 'printf "%s\n" "$1" > /data/.mm9-persistence' sh "${marker}"
  done
}

assert_persistence_markers() {
  local marker="$1"
  local actual

  actual="$(compose run --rm --no-deps postgres-check read-marker "${marker}")"
  [[ "${actual}" == "${marker}" ]] || {
    printf 'PostgreSQL marker не сохранился после restart.\n' >&2
    return 1
  }

  for service in redis-edge redis-auth; do
    actual="$(compose exec -T "${service}" \
      sh -ceu \
      'REDISCLI_AUTH="${REDIS_PASSWORD}" redis-cli --no-auth-warning GET mm9:persistence' \
      sh)"
    [[ "${actual}" == "${marker}" ]] || {
      printf '%s marker не сохранился после restart.\n' "${service}" >&2
      return 1
    }
  done

  for service in seaweed-quarantine seaweed-public seaweed-internal; do
    actual="$(compose exec -T "${service}" sh -ceu 'cat /data/.mm9-persistence' sh)"
    [[ "${actual}" == "${marker}" ]] || {
      printf '%s marker не сохранился после restart.\n' "${service}" >&2
      return 1
    }
  done
}

persistence() {
  local marker
  local observability_marker
  local observability_result
  local observability_trace_id
  local observability_span_id

  ready
  marker="mm9-persistence-$(date +%s)-$$"
  write_persistence_markers "${marker}"
  set_observability_endpoints
  observability_marker="mm17-persistence-$(date +%s)-$$"
  observability_result="$(emit_observability_marker "${observability_marker}")"
  IFS='|' read -r observability_trace_id observability_span_id <<<"${observability_result}"
  compose restart >/dev/null
  ready
  assert_persistence_markers "${marker}"
  assert_observability_marker \
    "${observability_marker}" \
    "${observability_trace_id}" \
    "${observability_span_id}"
  assert_grafana_provisioning
  printf 'Persistent volumes PostgreSQL, Redis, SeaweedFS, Tempo, Loki и Grafana пережили обычный restart.\n'
}

psql_rw() {
  ready
  compose exec \
    --env PGPASSWORD="$(awk -F= '$1 == "POSTGRES_APP_RW_PASSWORD" { print $2 }' "${ENV_FILE}")" \
    postgres-primary \
    psql --host postgres-rw --username app_rw --dbname marketmesh --no-psqlrc
}

psql_ro() {
  ready
  compose exec \
    --env PGPASSWORD="$(awk -F= '$1 == "POSTGRES_APP_RO_PASSWORD" { print $2 }' "${ENV_FILE}")" \
    postgres-primary \
    psql --host postgres-ro --username app_ro --dbname marketmesh --no-psqlrc
}

verify() {
  config
  up
  smoke
  persistence
}

case "${1:-}" in
  clean)
    ensure_env
    compose down --volumes --remove-orphans
    ;;
  config)
    config
    ;;
  down)
    ensure_env
    compose down --remove-orphans
    ;;
  env)
    ensure_env
    ;;
  logs)
    ensure_env
    compose logs --follow --tail=200
    ;;
  observability-clean)
    observability_clean
    ;;
  observability-config)
    validate_observability_configs
    ;;
  observability-down)
    observability_down
    ;;
  observability-logs)
    ensure_env
    compose logs --follow --tail=200 "${OBSERVABILITY_SERVICES[@]}"
    ;;
  observability-outage)
    observability_outage
    ;;
  observability-persistence)
    observability_persistence
    ;;
  observability-ready)
    observability_ready
    printf 'Все контейнеры observability запущены и прошли health checks.\n'
    ;;
  observability-smoke)
    observability_smoke
    ;;
  observability-status)
    ensure_env
    compose ps "${OBSERVABILITY_SERVICES[@]}"
    ;;
  observability-up)
    observability_up
    ;;
  observability-verify)
    observability_up
    observability_smoke
    observability_outage
    observability_persistence
    ;;
  persistence)
    persistence
    ;;
  psql-ro)
    psql_ro
    ;;
  psql-rw)
    psql_rw
    ;;
  ready)
    ready
    ;;
  smoke)
    smoke
    ;;
  status)
    ensure_env
    compose ps
    ;;
  up)
    up
    ;;
  verify)
    verify
    ;;
  *)
    usage
    exit 2
    ;;
esac
