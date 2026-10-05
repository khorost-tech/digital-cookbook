#!/usr/bin/env bash
# Сценарий 4: SQLite и DuckDB — обе встраиваемые, живут в процессе, без сети
# и без сервера. Сравнение имеет смысл только на ДВУХ запросах: аналитическая
# агрегация по всему набору (профиль колоночного хранения) и точечное чтение
# по первичному ключу (профиль строкового хранения). Только агрегация дала бы
# однобокий вывод «DuckDB быстрее» — этот скрипт гоняет оба запроса на
# одинаково засеянном 1 млн строк для обеих БД в одном процессе olap.
set -euo pipefail

# Windows/Git Bash: без этого docker compose иногда перегоняет POSIX-подобные
# пути в аргументах через путепреобразование MSYS.
export MSYS_NO_PATHCONV=1

ROWS="${ROWS:-1000000}"
N_AGG="${N_AGG:-10}"
N_POINT="${N_POINT:-20000}"
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUT:-$SELF_DIR/../fixtures/olap.txt}"

{
  echo "== SQLite vs DuckDB: агрегация и точечное чтение, rows=$ROWS =="
  date -u +"прогон: %Y-%m-%dT%H:%M:%SZ"
  docker compose exec -T bench olap -rows "$ROWS" -n-agg "$N_AGG" -n-point "$N_POINT"
} | tee "$OUT"

echo "фикстура записана: $OUT"
