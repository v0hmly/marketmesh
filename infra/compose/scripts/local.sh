#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly COMPOSE_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly COMPOSE_FILE="${COMPOSE_DIR}/compose.yml"
readonly ENV_FILE="${COMPOSE_DIR}/.env"

usage() {
  printf 'usage: %s {clean|config|down|env|logs|persistence|psql-ro|psql-rw|ready|smoke|status|up|verify}\n' "$0" >&2
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

  ready
  marker="mm9-persistence-$(date +%s)-$$"
  write_persistence_markers "${marker}"
  compose restart >/dev/null
  ready
  assert_persistence_markers "${marker}"
  printf 'Persistent volumes PostgreSQL, Redis и SeaweedFS пережили обычный restart.\n'
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
