#!/usr/bin/env bash

set -euo pipefail

readonly SERVICE_NAME="marketmesh-observability-smoke"
readonly SERVICE_VERSION="1.0.0"
readonly ENVIRONMENT="local"

usage() {
  printf 'usage: %s {emit|send|verify} ALLOY_HTTP TEMPO_HTTP LOKI_HTTP MARKER [TRACE_ID SPAN_ID]\n' "$0" >&2
}

require_tool() {
  local tool="$1"

  command -v "${tool}" >/dev/null 2>&1 || {
    printf 'Для observability smoke требуется %s.\n' "${tool}" >&2
    return 1
  }
}

send_trace() {
  local alloy_http="$1"
  local marker="$2"
  local trace_id="$3"
  local span_id="$4"
  local start_time="$5"
  local end_time="$6"

  curl --fail --silent --show-error --max-time 10 \
    --header 'Content-Type: application/json' \
    --data-binary @- \
    "${alloy_http}/v1/traces" >/dev/null <<EOF
{
  "resourceSpans": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"stringValue": "${SERVICE_NAME}"}},
        {"key": "service.version", "value": {"stringValue": "${SERVICE_VERSION}"}},
        {"key": "deployment.environment.name", "value": {"stringValue": "${ENVIRONMENT}"}},
        {"key": "service.instance.id", "value": {"stringValue": "${marker}"}}
      ]
    },
    "scopeSpans": [{
      "scope": {"name": "marketmesh/infra/compose/smoke", "version": "${SERVICE_VERSION}"},
      "spans": [{
        "traceId": "${trace_id}",
        "spanId": "${span_id}",
        "name": "MM-17 observability smoke",
        "kind": 2,
        "startTimeUnixNano": "${start_time}",
        "endTimeUnixNano": "${end_time}",
        "attributes": [
          {"key": "smoke.marker", "value": {"stringValue": "${marker}"}}
        ],
        "status": {"code": 1}
      }]
    }]
  }]
}
EOF
}

send_log() {
  local alloy_http="$1"
  local marker="$2"
  local trace_id="$3"
  local span_id="$4"
  local timestamp="$5"

  curl --fail --silent --show-error --max-time 10 \
    --header 'Content-Type: application/json' \
    --data-binary @- \
    "${alloy_http}/v1/logs" >/dev/null <<EOF
{
  "resourceLogs": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"stringValue": "${SERVICE_NAME}"}},
        {"key": "service.version", "value": {"stringValue": "${SERVICE_VERSION}"}},
        {"key": "deployment.environment.name", "value": {"stringValue": "${ENVIRONMENT}"}},
        {"key": "service.instance.id", "value": {"stringValue": "${marker}"}}
      ]
    },
    "scopeLogs": [{
      "scope": {"name": "marketmesh/infra/compose/smoke", "version": "${SERVICE_VERSION}"},
      "logRecords": [{
        "timeUnixNano": "${timestamp}",
        "observedTimeUnixNano": "${timestamp}",
        "severityNumber": 9,
        "severityText": "INFO",
        "body": {
          "stringValue": "{\"level\":\"info\",\"message\":\"MM-17 observability smoke\",\"trace_id\":\"${trace_id}\",\"span_id\":\"${span_id}\",\"smoke_marker\":\"${marker}\"}"
        },
        "attributes": [
          {"key": "smoke.marker", "value": {"stringValue": "${marker}"}}
        ],
        "traceId": "${trace_id}",
        "spanId": "${span_id}",
        "flags": 1
      }]
    }]
  }]
}
EOF
}

assert_tempo_trace() {
  local tempo_http="$1"
  local marker="$2"
  local trace_id="$3"
  local response

  for _ in {1..30}; do
    response="$(curl --fail --silent --max-time 5 "${tempo_http}/api/traces/${trace_id}" 2>/dev/null || true)"
    if [[ "${response}" == *"${SERVICE_NAME}"* && "${response}" == *"${marker}"* ]]; then
      return
    fi
    sleep 1
  done

  printf 'Trace %s не найден в Tempo или не содержит ожидаемые resource attributes.\n' "${trace_id}" >&2
  return 1
}

assert_loki_log() {
  local loki_http="$1"
  local marker="$2"
  local trace_id="$3"
  local response

  for _ in {1..30}; do
    response="$(curl --fail --silent --get --max-time 5 \
      --data-urlencode "query={service_name=\"${SERVICE_NAME}\"} | trace_id=\"${trace_id}\"" \
      --data-urlencode 'direction=backward' \
      --data-urlencode 'limit=20' \
      "${loki_http}/loki/api/v1/query_range" 2>/dev/null || true)"
    if [[ "${response}" == *"${trace_id}"* && "${response}" == *"${marker}"* ]]; then
      return
    fi
    sleep 1
  done

  printf 'Связанный log для trace %s не найден в Loki.\n' "${trace_id}" >&2
  return 1
}

verify() {
  local tempo_http="$1"
  local loki_http="$2"
  local marker="$3"
  local trace_id="$4"

  assert_tempo_trace "${tempo_http}" "${marker}" "${trace_id}"
  assert_loki_log "${loki_http}" "${marker}" "${trace_id}"
}

main() {
  local action="${1:-}"
  local alloy_http="${2:-}"
  local tempo_http="${3:-}"
  local loki_http="${4:-}"
  local marker="${5:-}"
  local trace_id="${6:-}"
  local span_id="${7:-}"
  local epoch_seconds
  local start_time
  local end_time

  if [[ -z "${alloy_http}" || -z "${tempo_http}" || -z "${loki_http}" || -z "${marker}" ]]; then
    usage
    return 2
  fi

  require_tool curl

  case "${action}" in
    emit|send)
      require_tool openssl
      trace_id="$(openssl rand -hex 16)"
      span_id="$(openssl rand -hex 8)"
      epoch_seconds="$(date +%s)"
      start_time="${epoch_seconds}000000000"
      end_time="${epoch_seconds}001000000"
      send_trace "${alloy_http}" "${marker}" "${trace_id}" "${span_id}" "${start_time}" "${end_time}"
      send_log "${alloy_http}" "${marker}" "${trace_id}" "${span_id}" "${end_time}"
      if [[ "${action}" == "emit" ]]; then
        verify "${tempo_http}" "${loki_http}" "${marker}" "${trace_id}"
      fi
      printf '%s|%s\n' "${trace_id}" "${span_id}"
      ;;
    verify)
      if [[ ! "${trace_id}" =~ ^[[:xdigit:]]{32}$ || ! "${span_id}" =~ ^[[:xdigit:]]{16}$ ]]; then
        usage
        return 2
      fi
      verify "${tempo_http}" "${loki_http}" "${marker}" "${trace_id}"
      ;;
    *)
      usage
      return 2
      ;;
  esac
}

main "$@"
