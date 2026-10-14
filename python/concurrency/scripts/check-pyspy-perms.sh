#!/usr/bin/env bash
# Какие права нужны py-spy на самом деле.
#
# Утверждение «нужен CAP_SYS_PTRACE» встречается повсеместно, но оно не
# универсально. Сценарий гоняет три случая БЕЗ возможности и С ней, чтобы
# разница была видна, а не пересказана.
#
# ВНИМАНИЕ: то, что здесь получится, зависит от ядра хоста. На ядрах с
# включённым модулем Yama (ptrace_scope=1 — умолчание многих дистрибутивов)
# результат будет другим, и сценарий это печатает в шапке.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

IMAGE="${IMAGE:-python-concurrency-bench:latest}"
out=fixtures/pyspy-perms.txt
bench_abs="$(pwd)/bench"

{
  echo "# Права py-spy: три сценария без CAP_SYS_PTRACE и с ней"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
} > "$out"

for mode in without with; do
  if [ "$mode" = "with" ]; then
    caps="--cap-add=SYS_PTRACE"
    echo "## С CAP_SYS_PTRACE" >> "$out"
  else
    caps=""
    echo "## БЕЗ CAP_SYS_PTRACE" >> "$out"
  fi
  # shellcheck disable=SC2086
  docker run --rm --init $caps \
    -v "${bench_abs}:/bench" "$IMAGE" sh /bench/pyspy_perms.sh 2>&1 \
    | tr -d '\r' >> "$out"
  echo "" >> "$out"
done

echo "готово: $out"
