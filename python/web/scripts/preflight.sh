#!/usr/bin/env bash
# Проверка среды ПЕРЕД замером.
#
# Требование чистой среды бесполезно, пока оно не проверяется: беглый сервер
# от прошлого прогона держит порт и отвечает на нагрузку, а по числам это
# выглядит как «фреймворк вдруг стал быстрее».
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

MIN_IDLE="${MIN_IDLE:-80}"
fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 1; }

dc() { docker compose exec -T bench sh -c "$1" | tr -d '\r'; }

# 1. Беглые процессы python: серверы прошлого прогона.
alive=$(dc "ps -eo stat=,comm= | awk '\$2 == \"python\" && \$1 !~ /^Z/' | wc -l")
[ "$alive" -eq 0 ] || fail "в контейнере ${alive} живых процессов python — серверы прошлого прогона не погашены"

# 2. Занятые порты целей: даже если процесс не python, порт должен быть свободен.
busy=""
for port in 8001 8002 8003 8004 8005; do
  if dc "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null" | awk -v p="$port" '
      NR>1 {split($2,a,":"); if (strtonum("0x" a[2]) == p && $4 == "0A") found=1}
      END {exit !found}'; then
    busy="${busy} ${port}"
  fi
done
[ -z "$busy" ] || fail "порты заняты:${busy}"

# 3. Занятость процессора. Меряется ПРОСТОЙ, а не loadavg: в WSL2 средняя
#    загрузка учитывает задачи в непрерываемом ожидании и показывает 2.2
#    при 91% простоя — гвард на ней блокировал бы годные замеры.
snap() { dc "awk '/^cpu /{print \$2+\$3+\$4+\$5+\$6+\$7+\$8, \$5}' /proc/stat"; }
a=$(snap); sleep 1; b=$(snap)
idle=$(echo "$a $b" | awk '{tot=$3-$1; if (tot<=0) {print 0; exit} printf "%.0f", ($4-$2)*100/tot}')
load=$(dc "cut -d' ' -f1 /proc/loadavg")
[ "$idle" -ge "$MIN_IDLE" ] || fail "простой процессора ${idle}% ниже порога ${MIN_IDLE}% (loadavg ${load})"

echo "PREFLIGHT OK: беглых серверов нет, порты свободны, простой процессора ${idle}% (loadavg ${load})"
