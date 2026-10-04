#!/usr/bin/env bash
# Артефакт 2 (cross-engine): входит ли ширина PK в размер ВТОРИЧНОГО индекса?
# Ответ зависит от архитектуры хранения:
#  - PostgreSQL: куча НЕ кластеризована по PK, вторичный btree хранит
#    (значение, ctid=6 байт). Ширина PK во вторичный индекс не попадает →
#    индекс по payload одинаков для 8- и 16-байтного PK.
#  - MySQL/InnoDB: таблица КЛАСТЕРИЗОВАНА по PK, вторичный индекс хранит
#    (значение, PK). 16-байтный PK попадает в КАЖДЫЙ leaf → индекс крупнее.
# Различие внутри каждого движка — только ширина PK. Между движками сравниваем
# НЕ абсолюты, а НАЛИЧИЕ/ОТСУТСТВИЕ эффекта. Один хост: показываем структурный
# факт (равны/не равны), не производительность.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
N=${N:-200000}
PSQL=(docker compose -f compose/compose.yml exec -T pg psql -U ids -d ids -tAX)
MYSQL=(docker compose -f compose/compose.yml exec -T mysql mysql -uroot -pids -N -B ids)

# --- Самодостаточность PG-половины: k_bigint и k_uuid4 должны иметь равное
# ненулевое число строк. Если пусто или не равны (напр. после свежего up) —
# наполняем обе через loader на одинаковом N, прежде чем строить вторичные
# индексы. Иначе можно сравнить пустые таблицы (0 == 0) и получить ложный
# сигнал "равны".
cb=$("${PSQL[@]}" -c "select count(*) from k_bigint;")
cu=$("${PSQL[@]}" -c "select count(*) from k_uuid4;")
echo "[demo] PG rows before: k_bigint=$cb k_uuid4=$cu" >&2
if [ "$cb" -eq 0 ] || [ "$cu" -eq 0 ] || [ "$cb" -ne "$cu" ]; then
  echo "[demo] PG-таблицы пусты или не равны — наполняем обе через loader (N=$N)..." >&2
  ( cd loader && go run . -key=bigint -n="$N" )
  ( cd loader && go run . -key=uuid4 -n="$N" )
  cb=$("${PSQL[@]}" -c "select count(*) from k_bigint;")
  cu=$("${PSQL[@]}" -c "select count(*) from k_uuid4;")
  echo "[demo] PG rows after load: k_bigint=$cb k_uuid4=$cu" >&2
fi

# --- PostgreSQL: вторичный индекс по payload на k_bigint и k_uuid4 ---
for t in k_bigint k_uuid4; do
  "${PSQL[@]}" -c "drop index if exists sx_$t; create index sx_$t on $t(payload);" >/dev/null
done
pgb=$("${PSQL[@]}" -c "select pg_relation_size('sx_k_bigint');")
pgu=$("${PSQL[@]}" -c "select pg_relation_size('sx_k_uuid4');")

# --- MySQL/InnoDB: наполнить обе таблицы одинаковым N, снять размер индекса sx ---
"${MYSQL[@]}" -e "SET SESSION cte_max_recursion_depth=1000000;
  TRUNCATE m_bigint; TRUNCATE m_uuid;
  INSERT INTO m_bigint(payload) WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n<$N) SELECT REPEAT('x',64) FROM s;
  INSERT INTO m_uuid(id,payload) WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n<$N) SELECT UUID_TO_BIN(UUID()), REPEAT('x',64) FROM s;
  ANALYZE TABLE m_bigint, m_uuid;" >/dev/null 2>&1 || { echo "DEMO FAILED: MySQL наполнение/ANALYZE не прошло"; exit 1; }
idxsize() { "${MYSQL[@]}" -e "SELECT stat_value*@@innodb_page_size FROM mysql.innodb_index_stats WHERE database_name='ids' AND table_name='$1' AND index_name='sx' AND stat_name='size';"; }
myb=$(idxsize m_bigint); myu=$(idxsize m_uuid)

printf "PostgreSQL secondary idx bytes: bigint=%s uuid=%s\n" "$pgb" "$pgu"
printf "MySQL/InnoDB secondary idx bytes: bigint=%s uuid=%s\n" "$myb" "$myu"

# Фальсификация КОНТРАСТА: сигнал засчитан только если PG-индексы РАВНЫ
# (ширина PK не дошла до вторичного) И MySQL-uuid-индекс КРУПНЕЕ bigint
# (кластеризованный PK дошёл). Провал любой половины = DEMO FAILED.
awk -v pb="$pgb" -v pu="$pgu" -v mb="$myb" -v mu="$myu" 'BEGIN{
  pg_equal = (pb+0 == pu+0);
  my_bigger = (mu+0 > mb+0);
  if (pg_equal && my_bigger) { print "SIGNAL: PG вторичный не зависит от ширины PK (равны); MySQL/InnoDB — зависит (uuid крупнее)"; exit 0 }
  print "DEMO FAILED: контраст не воспроизвёлся (pg_equal="pg_equal" my_bigger="my_bigger"; pb="pb" pu="pu" mb="mb" mu="mu")"; exit 1
}'
