#!/usr/bin/env bash

set -euo pipefail

# taskboard.sh запускает taskboard CLI с базой задач из репозитория,
# чтобы доска синхронизировалась между машинами вместе с git.

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

exec taskboard --db "${REPO_ROOT}/.taskboard/taskboard.db" "$@"
