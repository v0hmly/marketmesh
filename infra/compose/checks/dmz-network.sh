#!/bin/sh

set -eu

assert_reachable() {
  host="$1"
  port="$2"

  if ! nc -z -w 3 "${host}" "${port}"; then
    printf 'Ожидаемый DMZ endpoint недоступен: %s:%s\n' "${host}" "${port}" >&2
    exit 1
  fi
}

assert_unreachable() {
  host="$1"
  port="$2"

  if nc -z -w 2 "${host}" "${port}" >/dev/null 2>&1; then
    printf 'Нарушена изоляция: DMZ достиг %s:%s\n' "${host}" "${port}" >&2
    exit 1
  fi
}

assert_reachable edge-state 6379
assert_reachable object-quarantine 8333
assert_reachable object-public 8333
assert_reachable otel-collector 4317

assert_unreachable postgres-rw 5432
assert_unreachable postgres-ro 5432
assert_unreachable auth-state 6379
assert_unreachable object-internal 8333

printf 'Сетевая проверка пройдена: DMZ endpoints доступны, internal endpoints недостижимы.\n'
