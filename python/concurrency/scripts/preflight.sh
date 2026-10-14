#!/usr/bin/env bash
# Проверка среды ПЕРЕД замером.
#
# Требование "никаких посторонних процессов во время замера" бесполезно, пока
# оно не проверяется: беглый процесс от прошлого прогона незаметно съедает
# ядро и смещает результат, а по числам это выглядит как разброс.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

MIN_IDLE="${MIN_IDLE:-80}"
fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 1; }

# 1. Живые процессы python в контейнере. Зомби не в счёт: они не потребляют
#    CPU. Считаем только те, что могут работать.
alive=$(docker compose exec -T bench sh -c \
  "ps -eo stat=,comm= | awk '\$2 == \"python\" && \$1 !~ /^Z/' | wc -l" | tr -d '\r')
[ "$alive" -eq 0 ] || fail "в контейнере ${alive} живых процессов python — беглецы прошлого прогона"

# 2. Зомби: сами по себе безвредны, но их скопление означает, что пулы
#    процессов не подчищаются, и стоит пересоздать контейнер.
zombies=$(docker compose exec -T bench sh -c \
  "ps -eo stat= | grep -c '^Z' || true" | tr -d '\r')
if [ "$zombies" -gt 20 ]; then
  fail "${zombies} зомби-процессов — пересоздайте контейнер: docker compose up -d --force-recreate"
fi

# 3. Занятость процессора.
#
# Меряется ПРОСТОЙ процессора, а не средняя загрузка. В WSL2 loadavg
# учитывает задачи в непрерываемом ожидании и легко показывает 2.2 при
# 91% простоя: гвард на loadavg блокировал бы годный замер, а в другую
# сторону мог бы и пропустить занятую машину. Простой — прямая величина.
# loadavg всё равно печатается: он полезен в шапке фикстуры как контекст.
snap() { docker compose exec -T bench sh -c "awk '/^cpu /{print \$2+\$3+\$4+\$5+\$6+\$7+\$8, \$5}' /proc/stat" | tr -d ''; }
a=$(snap)
sleep 1
b=$(snap)
idle=$(echo "$a $b" | awk '{tot=$3-$1; if (tot<=0) {print 0; exit} printf "%.0f", ($4-$2)*100/tot}')
load=$(docker compose exec -T bench sh -c "cut -d' ' -f1 /proc/loadavg" | tr -d '')
[ "$idle" -ge "$MIN_IDLE" ] || fail "простой процессора ${idle}% ниже порога ${MIN_IDLE}% — машина занята (loadavg ${load})"

echo "PREFLIGHT OK: живых посторонних python нет, зомби ${zombies}, простой процессора ${idle}% (loadavg ${load})"
