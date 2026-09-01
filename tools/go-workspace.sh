#!/usr/bin/env bash

set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly REPOSITORY_MODULE_PREFIX="github.com/v0hmly/marketmesh"
readonly GO_VERSION="1.27.0"
readonly MODULES=(
  "api/gen/go|${REPOSITORY_MODULE_PREFIX}/api/gen/go"
  "api/tunnel|${REPOSITORY_MODULE_PREFIX}/api/tunnel"
  "e2e/tunnel|${REPOSITORY_MODULE_PREFIX}/e2e/tunnel"
  "platform|${REPOSITORY_MODULE_PREFIX}/platform"
  "services/auth|${REPOSITORY_MODULE_PREFIX}/services/auth"
  "services/gateway-in|${REPOSITORY_MODULE_PREFIX}/services/gateway-in"
  "services/gateway-out|${REPOSITORY_MODULE_PREFIX}/services/gateway-out"
  "services/user|${REPOSITORY_MODULE_PREFIX}/services/user"
)

usage() {
  echo "usage: $0 {arch|build|fmt|fmt-check|test|test-race|vet}" >&2
}

module_packages() {
  local module_directory="$1"

  (
    cd "${REPOSITORY_ROOT}/${module_directory}"
    if [[ -z "$(find . -type f -name '*.go' -print -quit)" ]]; then
      exit 0
    fi
    go list ./...
  )
}

run_go_command() {
  local operation="$1"
  local module
  local module_directory
  local packages

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    packages="$(module_packages "${module_directory}")"

    if [[ -z "${packages}" ]]; then
      echo "==> ${module_directory}: packages not found, skipped"
      continue
    fi

    echo "==> ${module_directory}: go ${operation} ./..."
    (
      cd "${REPOSITORY_ROOT}/${module_directory}"
      go "${operation}" ./...
    )
  done
}

run_tests_with_race() {
  local module
  local module_directory
  local packages

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    packages="$(module_packages "${module_directory}")"

    if [[ -z "${packages}" ]]; then
      echo "==> ${module_directory}: packages not found, skipped"
      continue
    fi

    echo "==> ${module_directory}: go test -race ./..."
    (
      cd "${REPOSITORY_ROOT}/${module_directory}"
      go test -race ./...
    )
  done
}

format_go_files() {
  find "${REPOSITORY_ROOT}" \
    -type d \( -name .git -o -name vendor -o -name node_modules \) -prune -o \
    -type f -name '*.go' -exec gofmt -w {} +
}

check_go_format() {
  local unformatted

  unformatted="$(
    find "${REPOSITORY_ROOT}" \
      -type d \( -name .git -o -name vendor -o -name node_modules \) -prune -o \
      -type f -name '*.go' -exec gofmt -l {} +
  )"

  if [[ -n "${unformatted}" ]]; then
    echo "Unformatted Go files:" >&2
    echo "${unformatted}" >&2
    return 1
  fi
}

verify_module_paths() {
  local module
  local module_directory
  local expected_path
  local actual_path

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    expected_path="${module#*|}"
    actual_path="$(awk '$1 == "module" { print $2 }' "${REPOSITORY_ROOT}/${module_directory}/go.mod")"

    if [[ "${actual_path}" != "${expected_path}" ]]; then
      echo "Invalid module path in ${module_directory}/go.mod: expected ${expected_path}, got ${actual_path}" >&2
      return 1
    fi
  done
}

verify_go_versions() {
  local module
  local module_directory
  local actual_version

  actual_version="$(awk '$1 == "go" { print $2 }' "${REPOSITORY_ROOT}/go.work")"
  if [[ "${actual_version}" != "${GO_VERSION}" ]]; then
    echo "Invalid Go version in go.work: expected ${GO_VERSION}, got ${actual_version}" >&2
    return 1
  fi

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    actual_version="$(awk '$1 == "go" { print $2 }' "${REPOSITORY_ROOT}/${module_directory}/go.mod")"

    if [[ "${actual_version}" != "${GO_VERSION}" ]]; then
      echo "Invalid Go version in ${module_directory}/go.mod: expected ${GO_VERSION}, got ${actual_version}" >&2
      return 1
    fi
  done
}

verify_workspace_modules() {
  local module
  local expected_path
  local actual_modules

  actual_modules="$(
    cd "${REPOSITORY_ROOT}"
    go list -m -f '{{.Path}}' | sort
  )"

  for module in "${MODULES[@]}"; do
    expected_path="${module#*|}"
    if ! grep -Fqx "${expected_path}" <<<"${actual_modules}"; then
      echo "Module ${expected_path} is absent from go.work" >&2
      return 1
    fi
  done

  if [[ "$(wc -l <<<"${actual_modules}" | tr -d ' ')" -ne "${#MODULES[@]}" ]]; then
    echo "go.work contains a module not declared in tools/go-workspace.sh" >&2
    return 1
  fi
}

verify_forbidden_imports() {
  local module
  local module_directory
  local module_path
  local packages
  local imported_path

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    module_path="${module#*|}"
    packages="$(module_packages "${module_directory}")"

    if [[ -z "${packages}" ]]; then
      continue
    fi

    while IFS= read -r imported_path; do
      case "${module_path}" in
        "${REPOSITORY_MODULE_PREFIX}/services/"*)
          if [[ "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/services/"* && "${imported_path}" != "${module_path}" && "${imported_path}" != "${module_path}/"* ]]; then
            echo "Forbidden service import: ${module_path} -> ${imported_path}" >&2
            return 1
          fi
          ;;
        "${REPOSITORY_MODULE_PREFIX}/platform")
          if [[ "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/services/"* || "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/api/gen/go"* ]]; then
            echo "Forbidden platform import: ${module_path} -> ${imported_path}" >&2
            return 1
          fi
          ;;
        "${REPOSITORY_MODULE_PREFIX}/api/gen/go")
          if [[ "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/services/"* || "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/platform"* ]]; then
            echo "Forbidden generated contract import: ${module_path} -> ${imported_path}" >&2
            return 1
          fi
          ;;
        "${REPOSITORY_MODULE_PREFIX}/api/tunnel")
          if [[ "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/services/"* || "${imported_path}" == "${REPOSITORY_MODULE_PREFIX}/platform"* ]]; then
            echo "Forbidden tunnel contract import: ${module_path} -> ${imported_path}" >&2
            return 1
          fi
          ;;
      esac
    done < <(
      cd "${REPOSITORY_ROOT}/${module_directory}"
      go list -deps -f '{{.ImportPath}}' ./...
    )
  done
}

verify_no_production_testkit_imports() {
  local module
  local module_directory
  local importer
  local imported
  local testkit_path="${REPOSITORY_MODULE_PREFIX}/platform/testkit"

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    if [[ -z "$(module_packages "${module_directory}")" ]]; then
      continue
    fi

    while IFS='|' read -r importer imported; do
      if [[ "${imported}" == "${testkit_path}" || "${imported}" == "${testkit_path}/"* ]]; then
        if [[ "${importer}" != "${testkit_path}" && "${importer}" != "${testkit_path}/"* ]]; then
          echo "Forbidden production testkit import: ${importer} -> ${imported}" >&2
          return 1
        fi
      fi
    done < <(
      cd "${REPOSITORY_ROOT}/${module_directory}"
      go list -f '{{ $package := .ImportPath }}{{ range .Imports }}{{ $package }}|{{ . }}{{ "\n" }}{{ end }}' ./...
    )
  done
}

verify_no_local_replaces() {
  local module
  local module_directory

  for module in "${MODULES[@]}"; do
    module_directory="${module%%|*}"
    if grep -Eq '=>[[:space:]]+(\.{1,2}/|/)' "${REPOSITORY_ROOT}/${module_directory}/go.mod"; then
      echo "Local replace directive is forbidden in ${module_directory}/go.mod" >&2
      return 1
    fi
  done
}

verify_architecture() {
  verify_module_paths
  verify_go_versions
  verify_workspace_modules
  verify_forbidden_imports
  verify_no_production_testkit_imports
  verify_no_local_replaces
  echo "Architecture checks passed"
}

case "${1:-}" in
  arch)
    verify_architecture
    ;;
  build)
    run_go_command build
    ;;
  fmt)
    format_go_files
    ;;
  fmt-check)
    check_go_format
    ;;
  test)
    run_go_command test
    ;;
  test-race)
    run_tests_with_race
    ;;
  vet)
    run_go_command vet
    ;;
  *)
    usage
    exit 2
    ;;
esac
