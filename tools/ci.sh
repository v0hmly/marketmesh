#!/usr/bin/env bash

set -euo pipefail

readonly REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=ci-versions.env
source "${REPO_ROOT}/tools/ci-versions.env"
export PATH="${REPO_ROOT}/bin:${PATH}"
cd "${REPO_ROOT}"

bootstrap() {
  # GOWORK=off не должен переключать auto toolchain на более старый системный Go.
  local toolchain
  toolchain="$(GOWORK="${REPO_ROOT}/backend/go.work" go env GOVERSION)"
  export GOTOOLCHAIN="${toolchain}"
  mkdir -p bin
  GOBIN="${REPO_ROOT}/bin" GOWORK=off go install "github.com/go-task/task/v3/cmd/task@v${TASK_VERSION}"
  GOBIN="${REPO_ROOT}/bin" GOWORK=off go install "github.com/rhysd/actionlint/cmd/actionlint@v${ACTIONLINT_VERSION}"
  GOBIN="${REPO_ROOT}/bin" GOWORK=off go install "github.com/zricethezav/gitleaks/v8@v${GITLEAKS_VERSION}"
  GOBIN="${REPO_ROOT}/bin" GOWORK=off go install "golang.org/x/vuln/cmd/govulncheck@v${GOVULNCHECK_VERSION}"
}

baseline_commit() {
  local baseline="${CI_BASE_REF:-origin/dev}"
  if [[ "${baseline}" == 0000000000000000000000000000000000000000 ]]; then
    baseline=origin/dev
  fi
  git rev-parse --verify "${baseline}^{commit}"
}

secrets() {
  local baseline
  baseline="$(baseline_commit)"
  # Включает все новые коммиты, даже если секрет удалён следующим коммитом.
  gitleaks git --redact --no-banner --log-opts="${baseline}..HEAD" .
}

check_tree() {
  git diff --exit-code HEAD -- .
  if [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
    printf '%s\n' 'CI обнаружил новые неигнорируемые файлы:' >&2
    git ls-files --others --exclude-standard >&2
    return 1
  fi
}

case "${1:-}" in
  bootstrap) bootstrap ;;
  workflow-lint)
    actionlint -shellcheck=''
    python3 tools/layout-check.py
    ;;
  go)
    task fmt-check arch vet test-race build
    ./backend/tools/go-workspace.sh isolated
    ./backend/tools/go-workspace.sh mod-verify
    ;;
  frontend)
    pnpm --dir frontend install --frozen-lockfile
    task frontend:verify
    ;;
  api)
    PROTO_BREAKING_REF="$(baseline_commit)"
    export PROTO_BREAKING_REF
    task api:verify
    ;;
  secrets) secrets ;;
  vulnerabilities)
    ./backend/tools/go-workspace.sh vuln
    pnpm --dir frontend audit --audit-level=high
    ;;
  check-tree) check_tree ;;
  *)
    printf 'Использование: %s <bootstrap|workflow-lint|go|frontend|api|secrets|vulnerabilities|check-tree>\n' "$0" >&2
    exit 2
    ;;
esac
