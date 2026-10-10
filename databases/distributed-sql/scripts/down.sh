#!/usr/bin/env bash
#
# down.sh [движок|all] — снести профиль вместе с томами. Без аргумента — все.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

target="${1:-all}"
list="$target"
[ "$target" = all ] && list="$ENGINES"
for e in $list; do
    docker compose -f "$(compose_of "$e")" down -v >/dev/null 2>&1 && log "$e снесён"
done
if [ "$target" = all ]; then
    docker network rm "$NET" >/dev/null 2>&1 || true
fi
