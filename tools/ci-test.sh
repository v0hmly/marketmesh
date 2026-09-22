#!/usr/bin/env bash

set -euo pipefail

readonly REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly FIXTURE="$(mktemp -d)"
trap 'rm -rf -- "${FIXTURE}"' EXIT

mkdir -p "${FIXTURE}/tools" "${FIXTURE}/bin"
cp "${REPO_ROOT}/tools/ci.sh" "${REPO_ROOT}/tools/ci-versions.env" "${FIXTURE}/tools/"
ln -s "${REPO_ROOT}/bin/gitleaks" "${FIXTURE}/bin/gitleaks"
cd "${FIXTURE}"
git init -q
git config user.name 'CI self-test'
git config user.email 'ci@example.invalid'
printf 'bin/\n' > .gitignore
printf 'initial\n' > tracked.txt
git add .
git -c core.hooksPath=/dev/null -c commit.gpgSign=false commit -qm initial
export CI_BASE_REF
CI_BASE_REF="$(git rev-parse HEAD)"

expect_failure() {
  if "$@" > "${FIXTURE}/bin/check.log" 2>&1; then
    printf 'Ожидалась ошибка: %s\n' "$*" >&2
    exit 1
  fi
}

./tools/ci.sh check-tree
./tools/ci.sh secrets
expect_failure env CI_BASE_REF=missing-baseline ./tools/ci.sh secrets
printf 'changed\n' >> tracked.txt
expect_failure ./tools/ci.sh check-tree
git restore tracked.txt
printf 'generated\n' > generated.txt
expect_failure ./tools/ci.sh check-tree
rm generated.txt

# Синтетический маркер формата PAT; значение никогда не выдавалось GitHub.
printf 'token=ghp_%s\n' "$(openssl rand -hex 18)" > accidental.txt
git add accidental.txt
git -c core.hooksPath=/dev/null -c commit.gpgSign=false commit -qm accidental
git rm -q accidental.txt
git -c core.hooksPath=/dev/null -c commit.gpgSign=false commit -qm removed
expect_failure ./tools/ci.sh secrets
if ! grep -q 'leaks found' "${FIXTURE}/bin/check.log"; then
  printf '%s\n' 'Сканер завершился без доказанного обнаружения секрета.' >&2
  exit 1
fi
./tools/ci.sh check-tree
printf '%s\n' 'CI self-test: dirty tree, untracked generation, invalid baseline и удалённый секрет обнаружены.'
