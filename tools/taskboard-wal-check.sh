#!/usr/bin/env bash

# Проверяет, что WAL базы Taskboard пуст.
#
# Непустой WAL означает изменения, не зафиксированные в основном .db.
# Git такие изменения не видит, а переключение ветки или pull подменяет
# основной .db под существующим WAL, что необратимо повреждает базу
# (прецедент: MM-71 и инцидент с потерей задач MM-101..MM-111).
#
# Режимы:
#   block — непустой WAL завершает скрипт ошибкой (pre-commit);
#   warn  — непустой WAL только выводит предупреждение (post-checkout).

set -euo pipefail

mode="${1:-block}"
repo_root="$(git rev-parse --show-toplevel)"
wal="${repo_root}/.taskboard/taskboard.db-wal"

if [ ! -s "${wal}" ]; then
  exit 0
fi

printf '%s\n' 'Обнаружен непустой WAL базы Taskboard: .taskboard/taskboard.db-wal' >&2
printf '%s\n' 'Изменения задач ещё не зафиксированы в основном .db и невидимы для git.' >&2
printf '%s\n' 'Перед коммитом или переключением веток выполните: task taskboard:checkpoint' >&2

if [ "${mode}" = "block" ]; then
  exit 1
fi
exit 0
