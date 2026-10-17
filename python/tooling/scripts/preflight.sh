#!/usr/bin/env bash
# Проверка среды ПЕРЕД замером.
#
# Главное отличие от предыдущих стендов — кэши. Здесь они не помеха, а
# предмет измерения, поэтому preflight их не чистит, а ПОКАЗЫВАЕТ: сценарий
# сам приводит их в нужное состояние, а его задача — чтобы состояние было
# видно в фикстуре, а не подразумевалось.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

MIN_IDLE="${MIN_IDLE:-80}"
fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 1; }

dc() { docker compose exec -T bench sh -c "$1" | tr -d '\r'; }

# 1. Беглые процессы: установка прошлого прогона, не доведённая до конца,
#    держала бы файлы и искажала следующий замер.
alive=$(dc "ps -eo stat=,comm= | awk '\$2 ~ /^(uv|poetry|pip|python)/ && \$1 !~ /^Z/' | wc -l")
[ "$alive" -eq 0 ] || fail "в контейнере ${alive} живых процессов установки — прошлый прогон не завершён"

# 2. Локальный индекс отвечает. Без него инструменты молча уйдут в PyPI, и
#    замер превратится в замер сети — ровно то, от чего индекс и заведён.
code=$(dc "curl -sS -o /dev/null -w '%{http_code}' \"\$LOCAL_INDEX/\"" || true)
[ "$code" = "200" ] || fail "локальный индекс не отвечает (код ${code:-нет ответа})"

projects=$(dc "ls /index/simple | wc -l")
[ "$projects" -ge 50 ] || fail "в индексе всего ${projects} проектов — он не собран целиком"

# 3. Кэши: показываем размеры, а не чистим. Состояние задаёт сценарий.
sizes=$(dc "du -sm /caches/uv /caches/poetry /caches/pip /venvs 2>/dev/null | tr '\n' ' '" || true)

# 4. Занятость процессора. Меряется ПРОСТОЙ, а не loadavg: в WSL2 средняя
#    загрузка показывает 2.2 при 91% простоя.
snap() { dc "awk '/^cpu /{print \$2+\$3+\$4+\$5+\$6+\$7+\$8, \$5}' /proc/stat"; }
a=$(snap); sleep 1; b=$(snap)
idle=$(echo "$a $b" | awk '{tot=$3-$1; if (tot<=0) {print 0; exit} printf "%.0f", ($4-$2)*100/tot}')
load=$(dc "cut -d' ' -f1 /proc/loadavg")
[ "$idle" -ge "$MIN_IDLE" ] || fail "простой процессора ${idle}% ниже порога ${MIN_IDLE}% (loadavg ${load})"

echo "PREFLIGHT OK: беглых установок нет, индекс отдаёт ${projects} проектов, простой процессора ${idle}% (loadavg ${load})"
echo "  кэши, МиБ: ${sizes}"
