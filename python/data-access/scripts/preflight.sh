#!/usr/bin/env bash
# Проверка среды ПЕРЕД замером.
#
# Правила перенесены из волн 1 и 2, где каждое что-то поймало: беглый
# процесс от прошлого сценария, занятый порт, машина под посторонней
# нагрузкой. Добавлена проверка, которой там не было и которая нужна
# именно здесь: набор данных обязан быть тем же самым.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

MIN_IDLE="${MIN_IDLE:-80}"
fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 1; }

dc() { docker compose exec -T bench sh -c "$1" | tr -d '\r'; }

# 1. Беглые процессы python: плечи прошлого сценария.
alive=$(dc "ps -eo stat=,comm= | awk '\$2 == \"python\" && \$1 !~ /^Z/' | wc -l")
[ "$alive" -eq 0 ] || fail "в контейнере ${alive} живых процессов python — плечи прошлого сценария не погашены"

# 2. База отвечает и это ожидаемая версия.
pgver=$(dc "psql \"\$DSN\" -tAc 'SHOW server_version'" || true)
[ -n "$pgver" ] || fail "база не отвечает"
case "$pgver" in
  18*) ;;
  *) fail "ожидалась PostgreSQL 18, отвечает ${pgver}" ;;
esac

# 3. Ни одной посторонней сессии, кроме нашей: чужая транзакция держит
#    снимок и меняет и планы, и время. Своя же может остаться от
#    оборванного сценария.
others=$(dc "psql \"\$DSN\" -tAc \"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()\"")
[ "$others" -eq 0 ] || fail "к базе подключены ${others} посторонних сессий — оборванный сценарий держит соединения"

# 4. Набор данных тот же. Сверяется СОДЕРЖИМОЕ базы, а не число строк:
#    миграция из сценария Alembic могла добавить столбец и не откатиться,
#    и на счётчике строк это не видно.
if ! dc "cd /db && DSN=\"\$DSN\" python seed.py --verify" >/dev/null 2>&1; then
  fail "набор данных не совпадает с ожидаемым — пересейте базу (scripts/seed.sh)"
fi

# 5. Занятость процессора. Меряется ПРОСТОЙ, а не loadavg: в WSL2 средняя
#    загрузка учитывает задачи в непрерываемом ожидании и показывает 2.2
#    при 91% простоя — гвард на ней блокировал бы годные замеры.
snap() { dc "awk '/^cpu /{print \$2+\$3+\$4+\$5+\$6+\$7+\$8, \$5}' /proc/stat"; }
a=$(snap); sleep 1; b=$(snap)
idle=$(echo "$a $b" | awk '{tot=$3-$1; if (tot<=0) {print 0; exit} printf "%.0f", ($4-$2)*100/tot}')
load=$(dc "cut -d' ' -f1 /proc/loadavg")
[ "$idle" -ge "$MIN_IDLE" ] || fail "простой процессора ${idle}% ниже порога ${MIN_IDLE}% (loadavg ${load})"

echo "PREFLIGHT OK: беглых плеч нет, PostgreSQL ${pgver}, набор данных совпадает, простой процессора ${idle}% (loadavg ${load})"
