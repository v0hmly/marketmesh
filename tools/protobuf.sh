#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

# shellcheck source=protobuf-versions.env
source "${SCRIPT_DIR}/protobuf-versions.env"

readonly CACHE_DIR="${PROTO_CACHE_DIR:-${REPO_ROOT}/.cache/protobuf}"
readonly BIN_DIR="${CACHE_DIR}/bin"
readonly DOWNLOAD_DIR="${CACHE_DIR}/downloads"
readonly EASYP_BIN="${BIN_DIR}/easyp"
readonly PROTOC_BIN="${BIN_DIR}/protoc"
readonly PROTOC_GEN_ES_BIN="${REPO_ROOT}/node_modules/.bin/protoc-gen-es"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi

  shasum -a 256 "$1" | awk '{print $1}'
}

download() {
  local url="$1"
  local destination="$2"
  local expected_sha="$3"

  if [[ -f "${destination}" ]] && [[ "$(sha256_file "${destination}")" == "${expected_sha}" ]]; then
    return
  fi

  curl --fail --silent --show-error --location "${url}" --output "${destination}"

  local actual_sha
  actual_sha="$(sha256_file "${destination}")"
  if [[ "${actual_sha}" != "${expected_sha}" ]]; then
    printf 'Ошибка checksum для %s: ожидался %s, получен %s\n' \
      "${destination}" "${expected_sha}" "${actual_sha}" >&2
    return 1
  fi
}

platform() {
  local os
  local arch
  os="$(uname -s)"
  arch="$(uname -m)"

  case "${os}/${arch}" in
    Darwin/arm64)
      printf '%s\n' 'darwin-arm64 osx-aarch_64 d1dfa970550a28ae8228594c9b7c33907c804c462ed9b44ce668d67967ae1551 193289af0470c6a1aada357d4fba0bbf8d78bfaac8b5e42ca30af2ef75583de2'
      ;;
    Darwin/x86_64)
      printf '%s\n' 'darwin-amd64 osx-x86_64 de0ad10799ee87e06dac5f12c137522b15cee5b3d2520e1005fe21a4a2708811 537d73604a344ded6fc94e98e07e529d4fe3e4a0b09e59905353950fafc2a1f7'
      ;;
    Linux/aarch64 | Linux/arm64)
      printf '%s\n' 'linux-arm64 linux-aarch_64 63411731d2d5fd5187870e1f820e17ad05047089dd810281bc9016430c6d6772 01bf9d08808c7f96678b63f4bd8efa559bb4f83d5a7a270d5edaf507f9d5d9cf'
      ;;
    Linux/x86_64 | Linux/amd64)
      printf '%s\n' 'linux-amd64 linux-x86_64 4eafa8f91134710a405a568671b6776e8b984f21d846e9d65485d09fc0fde557 6930ebf62bd4ea607b98fff052596c6ee564b9835b4ce172c75a3f53ae9d91b7'
      ;;
    *)
      printf 'Неподдерживаемая платформа protobuf toolchain: %s/%s\n' "${os}" "${arch}" >&2
      return 1
      ;;
  esac
}

install_easyp() {
  if [[ -x "${EASYP_BIN}" ]] && "${EASYP_BIN}" --version 2>&1 | grep -q "v${EASYP_VERSION}"; then
    return
  fi

  local easyp_platform="$1"
  local expected_sha="$2"
  local archive="${DOWNLOAD_DIR}/easyp-${EASYP_VERSION}-${easyp_platform}.tar.gz"
  local extract_dir

  download \
    "https://github.com/easyp-tech/easyp/releases/download/v${EASYP_VERSION}/easyp-${EASYP_VERSION}-${easyp_platform}.tar.gz" \
    "${archive}" \
    "${expected_sha}"

  extract_dir="$(mktemp -d "${CACHE_DIR}/easyp.XXXXXX")"
  tar -xzf "${archive}" -C "${extract_dir}"
  install -m 0755 \
    "${extract_dir}/easyp-${EASYP_VERSION}-${easyp_platform}/easyp" \
    "${EASYP_BIN}"
  rm -rf -- "${extract_dir}"
}

install_protoc() {
  if [[ -x "${PROTOC_BIN}" ]] && [[ "$("${PROTOC_BIN}" --version)" == "libprotoc ${PROTOC_VERSION}" ]]; then
    return
  fi

  local protoc_platform="$1"
  local expected_sha="$2"
  local archive="${DOWNLOAD_DIR}/protoc-${PROTOC_VERSION}-${protoc_platform}.zip"
  local extract_dir
  local include_dir="${CACHE_DIR}/protoc/${PROTOC_VERSION}/include"

  download \
    "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-${protoc_platform}.zip" \
    "${archive}" \
    "${expected_sha}"

  extract_dir="$(mktemp -d "${CACHE_DIR}/protoc.XXXXXX")"
  unzip -q "${archive}" -d "${extract_dir}"
  install -m 0755 "${extract_dir}/bin/protoc" "${PROTOC_BIN}"
  mkdir -p "${include_dir}"
  cp -R "${extract_dir}/include/." "${include_dir}/"
  rm -rf -- "${extract_dir}"
}

install_go_plugin() {
  local binary="$1"
  local module="$2"
  local version="$3"
  local expected_output="$4"

  if [[ -x "${BIN_DIR}/${binary}" ]] && "${BIN_DIR}/${binary}" --version 2>&1 | grep -qx "${expected_output}"; then
    return
  fi

  GOBIN="${BIN_DIR}" go install "${module}@v${version}"
}

install_es_plugin() {
  local configured_version
  configured_version="$(
    cd "${REPO_ROOT}"
    node -p "require('./package.json').devDependencies['@bufbuild/protoc-gen-es']" 2>/dev/null || true
  )"
  if [[ "${configured_version}" != "${PROTOC_GEN_ES_VERSION}" ]]; then
    printf 'package.json должен фиксировать @bufbuild/protoc-gen-es версии %s\n' \
      "${PROTOC_GEN_ES_VERSION}" >&2
    return 1
  fi

  if [[ -x "${PROTOC_GEN_ES_BIN}" ]] && "${PROTOC_GEN_ES_BIN}" --version 2>&1 | grep -q "v${PROTOC_GEN_ES_VERSION}"; then
    return
  fi

  (
    cd "${REPO_ROOT}"
    pnpm install --frozen-lockfile
  )
}

bootstrap() {
  mkdir -p "${BIN_DIR}" "${DOWNLOAD_DIR}"

  local platform_values
  local easyp_platform
  local protoc_platform
  local easyp_sha
  local protoc_sha
  platform_values="$(platform)"
  read -r easyp_platform protoc_platform easyp_sha protoc_sha <<<"${platform_values}"

  install_easyp "${easyp_platform}" "${easyp_sha}"
  install_protoc "${protoc_platform}" "${protoc_sha}"
  install_go_plugin \
    protoc-gen-go \
    google.golang.org/protobuf/cmd/protoc-gen-go \
    "${PROTOC_GEN_GO_VERSION}" \
    "protoc-gen-go v${PROTOC_GEN_GO_VERSION}"
  install_go_plugin \
    protoc-gen-go-grpc \
    google.golang.org/grpc/cmd/protoc-gen-go-grpc \
    "${PROTOC_GEN_GO_GRPC_VERSION}" \
    "protoc-gen-go-grpc ${PROTOC_GEN_GO_GRPC_VERSION}"
  install_go_plugin \
    protoc-gen-connect-go \
    connectrpc.com/connect/cmd/protoc-gen-connect-go \
    "${PROTOC_GEN_CONNECT_GO_VERSION}" \
    "${PROTOC_GEN_CONNECT_GO_VERSION}"
  install_es_plugin
}

run_easyp() {
  (
    cd "${REPO_ROOT}"
    EASYPPATH="${CACHE_DIR}/easyp" \
      PROTO_TOOLS_BIN="${BIN_DIR}" \
      PROTOC_GEN_ES_PATH="${PROTOC_GEN_ES_BIN}" \
      "${EASYP_BIN}" --cfg "${REPO_ROOT}/easyp.yaml" "$@"
  )
}

verify_pins() {
  local generator_version
  local runtime_version

  generator_version="$(
    cd "${REPO_ROOT}"
    node -p "require('./package.json').devDependencies['@bufbuild/protoc-gen-es']"
  )"
  runtime_version="$(
    cd "${REPO_ROOT}"
    node -p "require('./api/gen/ts/package.json').dependencies['@bufbuild/protobuf']"
  )"

  if [[ "${generator_version}" != "${PROTOC_GEN_ES_VERSION}" ]]; then
    printf 'Версия protoc-gen-es не совпадает с tools/protobuf-versions.env.\n' >&2
    return 1
  fi
  if [[ "${runtime_version}" != "${PROTOBUF_ES_VERSION}" ]]; then
    printf 'Версия @bufbuild/protobuf не совпадает с tools/protobuf-versions.env.\n' >&2
    return 1
  fi
  if ! grep -Fq "github.com/googleapis/googleapis@${GOOGLEAPIS_REVISION}" "${REPO_ROOT}/easyp.yaml"; then
    printf 'Ревизия googleapis в easyp.yaml не совпадает с tools/protobuf-versions.env.\n' >&2
    return 1
  fi
  if [[ ! -s "${REPO_ROOT}/easyp.lock" ]] || \
    ! grep -Fq "${GOOGLEAPIS_REVISION}" "${REPO_ROOT}/easyp.lock"; then
    printf 'easyp.lock отсутствует или не фиксирует ожидаемую ревизию googleapis.\n' >&2
    return 1
  fi
}

clean_generated() {
  local go_out="$1"
  local ts_out="$2"

  mkdir -p "${go_out}" "${ts_out}"
  find "${go_out}" -type f \( -name '*.pb.go' -o -name '*.connect.go' \) -delete
  find "${ts_out}" -type f -name '*_pb.ts' -delete
}

generate_to() {
  local go_out="$1"
  local ts_out="$2"

  clean_generated "${REPO_ROOT}/${go_out}" "${REPO_ROOT}/${ts_out}"
  (
    cd "${REPO_ROOT}"
    EASYPPATH="${CACHE_DIR}/easyp" \
      PROTO_TOOLS_BIN="${BIN_DIR}" \
      PROTOC_GEN_ES_PATH="${PROTOC_GEN_ES_BIN}" \
      PROTO_GO_OUT="${go_out}" \
      PROTO_TS_OUT="${ts_out}" \
      "${EASYP_BIN}" --cfg "${REPO_ROOT}/easyp.yaml" generate
  )
}

copy_generated() {
  local source_dir="$1"
  local destination_dir="$2"
  shift 2

  mkdir -p "${destination_dir}"
  while IFS= read -r -d '' source_file; do
    local relative_path="${source_file#"${source_dir}/"}"
    mkdir -p "${destination_dir}/$(dirname -- "${relative_path}")"
    cp "${source_file}" "${destination_dir}/${relative_path}"
  done < <(find "${source_dir}" -type f "$@" -print0)
}

generate_check() (
  local check_dir
  local relative_check_dir
  check_dir="$(mktemp -d "${CACHE_DIR}/generate-check.XXXXXX")"
  relative_check_dir="${check_dir#"${REPO_ROOT}/"}"
  trap 'rm -rf -- "${check_dir}"' EXIT

  mkdir -p \
    "${check_dir}/expected/go" \
    "${check_dir}/expected/ts" \
    "${check_dir}/actual/go" \
    "${check_dir}/actual/ts"

  copy_generated \
    "${REPO_ROOT}/api/gen/go" \
    "${check_dir}/expected/go" \
    \( -name '*.pb.go' -o -name '*.connect.go' \)
  copy_generated \
    "${REPO_ROOT}/api/gen/ts" \
    "${check_dir}/expected/ts" \
    -name '*_pb.ts'

  generate_to "${relative_check_dir}/actual/go" "${relative_check_dir}/actual/ts"

  if ! diff -ru "${check_dir}/expected" "${check_dir}/actual"; then
    printf 'Сгенерированные API-файлы устарели. Выполните task api:generate.\n' >&2
    return 1
  fi
)

self_test() (
  local test_dir
  test_dir="$(mktemp -d "${CACHE_DIR}/self-test.XXXXXX")"
  trap 'rm -rf -- "${test_dir}"' EXIT

  mkdir -p "${test_dir}/proto/toolchain/v1"

  printf '%s\n' \
    'lint:' \
    '  use:' \
    '    - MINIMAL' \
    '    - BASIC' \
    '    - DEFAULT' \
    '    - COMMENTS' \
    '  enum_zero_value_suffix: UNSPECIFIED' \
    '  service_suffix: Service' \
    '  allow_comment_ignores: false' \
    'generate:' \
    '  inputs:' \
    '    - directory:' \
    '        path: .' \
    '        root: proto' \
    '  plugins:' \
    "    - path: ${BIN_DIR}/protoc-gen-go" \
    '      out: ${SELF_TEST_GO_OUT:-gen/first/go}' \
    '      opts:' \
    '        module: example.com/toolchain' \
    "    - path: ${BIN_DIR}/protoc-gen-go-grpc" \
    '      out: ${SELF_TEST_GO_OUT:-gen/first/go}' \
    '      opts:' \
    '        module: example.com/toolchain' \
    "    - path: ${BIN_DIR}/protoc-gen-connect-go" \
    '      out: ${SELF_TEST_GO_OUT:-gen/first/go}' \
    '      opts:' \
    '        module: example.com/toolchain' \
    "    - path: ${PROTOC_GEN_ES_BIN}" \
    '      out: ${SELF_TEST_TS_OUT:-gen/first/ts}' \
    '      opts:' \
    '        target: ts' \
    'breaking:' \
    '  against_git_ref: baseline' \
    >"${test_dir}/easyp.yaml"

  printf '%s\n' \
    'syntax = "proto3";' \
    '' \
    'package toolchain.v1;' \
    '' \
    'option go_package = "example.com/toolchain/toolchain/v1;toolchainv1";' \
    '' \
    '// ToolchainService verifies every configured code generator.' \
    'service ToolchainService {' \
    '  // Check verifies unary request and response generation.' \
    '  rpc Check(CheckRequest) returns (CheckResponse);' \
    '}' \
    '' \
    '// CheckRequest is the input of the toolchain self-test.' \
    'message CheckRequest {' \
    '  // Value is copied into the response.' \
    '  string value = 1;' \
    '}' \
    '' \
    '// CheckResponse is the output of the toolchain self-test.' \
    'message CheckResponse {' \
    '  // Value is copied from the request.' \
    '  string value = 1;' \
    '}' \
    >"${test_dir}/proto/toolchain/v1/toolchain.proto"

  (
    cd "${test_dir}"
    EASYPPATH="${CACHE_DIR}/easyp" "${EASYP_BIN}" validate-config >/dev/null
    EASYPPATH="${CACHE_DIR}/easyp" "${EASYP_BIN}" lint --root proto --path . >/dev/null
    EASYPPATH="${CACHE_DIR}/easyp" "${EASYP_BIN}" generate >/dev/null
    SELF_TEST_GO_OUT=gen/second/go \
      SELF_TEST_TS_OUT=gen/second/ts \
      EASYPPATH="${CACHE_DIR}/easyp" \
      "${EASYP_BIN}" generate >/dev/null
    diff -ru gen/first gen/second >/dev/null

    git init --quiet
    git config user.name 'MarketMesh protobuf self-test'
    git config user.email 'protobuf-self-test@marketmesh.invalid'
    git add easyp.yaml proto
    git commit --quiet -m baseline
    git tag baseline

    awk '{gsub("string value = 1;", "string BadField = 1;"); print}' \
      proto/toolchain/v1/toolchain.proto >proto/toolchain/v1/toolchain.proto.tmp
    mv proto/toolchain/v1/toolchain.proto.tmp proto/toolchain/v1/toolchain.proto
    if EASYPPATH="${CACHE_DIR}/easyp" "${EASYP_BIN}" lint --root proto --path . >/dev/null 2>&1; then
      printf 'EasyP lint принял заведомо некорректную схему.\n' >&2
      return 1
    fi

    git show baseline:proto/toolchain/v1/toolchain.proto >proto/toolchain/v1/toolchain.proto
    awk '$0 != "  string value = 1;"' \
      proto/toolchain/v1/toolchain.proto >proto/toolchain/v1/toolchain.proto.tmp
    mv proto/toolchain/v1/toolchain.proto.tmp proto/toolchain/v1/toolchain.proto
    if EASYPPATH="${CACHE_DIR}/easyp" "${EASYP_BIN}" breaking --path proto >/dev/null 2>&1; then
      printf 'EasyP breaking не обнаружил удаление поля v1.\n' >&2
      return 1
    fi
  )
)

print_versions() {
  printf 'EasyP: %s\n' "$("${EASYP_BIN}" --version)"
  printf 'protoc: %s\n' "$("${PROTOC_BIN}" --version)"
  printf 'protoc-gen-go: %s\n' "$("${BIN_DIR}/protoc-gen-go" --version)"
  printf 'protoc-gen-go-grpc: %s\n' "$("${BIN_DIR}/protoc-gen-go-grpc" --version)"
  printf 'protoc-gen-connect-go: %s\n' "$("${BIN_DIR}/protoc-gen-connect-go" --version)"
  printf 'protoc-gen-es: %s\n' "$("${PROTOC_GEN_ES_BIN}" --version)"
}

usage() {
  printf 'Использование: %s <bootstrap|versions|config|deps|deps-update|lint|generate|breaking|generate-check|self-test>\n' "$0" >&2
}

main() {
  local command="${1:-}"
  if [[ -z "${command}" ]]; then
    usage
    return 2
  fi

  bootstrap

  case "${command}" in
    bootstrap)
      ;;
    versions)
      print_versions
      ;;
    config)
      verify_pins
      run_easyp validate-config
      ;;
    deps)
      verify_pins
      run_easyp mod download
      ;;
    deps-update)
      run_easyp mod update
      verify_pins
      ;;
    lint)
      run_easyp lint --root api/proto --path .
      ;;
    generate)
      generate_to api/gen/go api/gen/ts
      ;;
    breaking)
      run_easyp breaking --path api/proto
      ;;
    generate-check)
      generate_check
      ;;
    self-test)
      self_test
      ;;
    *)
      usage
      return 2
      ;;
  esac
}

main "$@"
