#!/usr/bin/env bash
#
# 05-geo.sh — артефакт к статье «Геораспределение: partitioning, zone
# configs и survival goals».
#
# ЧТО ЗДЕСЬ ИЗМЕРЯЕТСЯ, А ЧТО НЕТ — читать до того, как брать числа.
#
# Измеряется МЕХАНИКА РАЗМЕЩЕНИЯ, и она настоящая:
#   A. три региона из меток --locality, девять узлов;
#   B. REGIONAL BY ROW: строка получает регион узла-шлюза, через который
#      пришла, и её реплики уезжают в этот регион;
#   C. SURVIVE ZONE FAILURE против SURVIVE REGION FAILURE: фактор
#      репликации меняется 3 -> 5, реплики растягиваются по регионам;
#   D. latency записи и чтения через разные шлюзы.
#
# НЕ измеряется ЦЕНА РАССТОЯНИЯ. Все девять узлов стоят на одной машине,
# межрегиональной задержки между ними нет и взяться ей неоткуда:
# эмуляция через tc netem невозможна, потому что ядро WSL2
# (5.15.167.4-microsoft-standard-WSL2) собрано без планировщиков трафика
# вообще — ни netem, ни prio, каталога модулей нет. Проверено, см.
# FIXTURES.md.
#
# Поэтому раздел D показывает ПОЛ этих величин: чего они стоят без
# географии. Разницу, которую даёт география, отсюда брать нельзя.
#
# Базовый кластер на время прогона останавливается: двенадцать узлов на
# одной машине мешали бы друг другу.
#
# Запуск (из databases/cockroachdb/):  bash scripts/05-geo.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

GEO_COMPOSE="compose/geo.yml"
PRIMARY_NODE=geo1
DB_NAME=geo
NODES="geo1 geo2 geo3 geo4 geo5 geo6 geo7 geo8 geo9"

cleanup() {
    log "снимаем гео-кластер и возвращаем базовый…"
    docker compose -f "$GEO_COMPOSE" down -v --remove-orphans >/dev/null 2>&1 || true
    docker compose -f compose/compose.yml start >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "останавливаем базовый кластер на время прогона…"
docker compose -f compose/compose.yml stop >/dev/null 2>&1 || true

log "поднимаем девять узлов…"
docker compose -f "$GEO_COMPOSE" up -d >/dev/null

log "инициализация кластера…"
initialized=0
for _ in $(seq 1 40); do
    out="$(docker exec geo1 cockroach init --insecure --host=localhost:26257 2>&1 || true)"
    if printf '%s' "$out" | grep -qE 'successfully initialized|already been initialized'; then
        initialized=1; break
    fi
    sleep 3
done
[ "$initialized" -eq 1 ] || fail "гео-кластер не инициализировался"

for n in $NODES; do wait_healthy "$n" 60; done
live="$(nodes_live)"
[ "$live" -eq 9 ] || fail "ожидали 9 живых узлов, кластер видит: $live"
log "живых узлов: $live"

# Схему накатываем ДО первых запросов: psql_c ходит в базу geo, и до её
# создания любой запрос упал бы с «database geo does not exist».
log "накатываем многорегиональную схему…"
docker exec -i geo1 cockroach sql --insecure --host=localhost:26257 < sql/05-geo.sql >/dev/null

echo
echo "=== A. Регионы и узлы ==="
echo "--- контейнер -> node_id:"
node_map
psql_c -e "SELECT * FROM [SHOW REGIONS FROM CLUSTER];"

# Проверяем, что реальной задержки между «регионами» нет: без этой
# проверки раздел D читался бы как замер географии.
log "фактический RTT между «регионами» (задержки не эмулируются):"
for pair in "geo1 geo2 внутри-ru-central" "geo1 geo4 ru-central->ru-west" "geo1 geo7 ru-central->ru-east"; do
    set -- $pair
    ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$2")"
    rtt="$(docker run --rm --network "container:$1" nicolaka/netshoot \
             ping -c 5 -q "$ip" 2>/dev/null | awk -F'/' '/rtt|round-trip/ { print $5 }')"
    printf '  %-28s RTT ср. %s мс\n' "$3" "${rtt:-НЕ ИЗМЕРЕН}"
done

echo
echo "=== Многорегиональная схема ==="
psql_c -e "SELECT * FROM [SHOW REGIONS FROM DATABASE geo];"
regions="$(sql_value "SELECT count(*) FROM [SHOW REGIONS FROM DATABASE geo];")"
[ "$regions" -eq 3 ] || fail "ожидали 3 региона в базе geo, нашли: $regions"
log "регионов в базе geo: $regions"

log "собираем loadgen…"
OUT=/tmp/crdb-05
mkdir -p "$OUT"
( cd loadgen && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

measure() { # $1 = метка, $2 = порт шлюза, $3 = режим
    "$OUT/loadgen" -mode "$3" -workers 8 -duration 20s \
        -dsn "postgresql://root@127.0.0.1:${2}/geo?sslmode=disable" > "$OUT/$1.json"
    awk -F'[:,]' '{ for (i = 1; i < NF; i++) {
                        if ($i ~ /"ops"/)    o = $(i+1);
                        if ($i ~ /"p50_ms"/) p50 = $(i+1);
                        if ($i ~ /"p95_ms"/) p95 = $(i+1);
                    } }
         END { printf "  %-32s ops=%-7s p50=%-9.1f p95=%.1f\n", L, o, p50, p95 }' \
        L="$1" "$OUT/$1.json"
}

echo
echo "=== B. REGIONAL BY ROW: строка получает регион своего шлюза ==="
psql_c -e "SHOW CREATE TABLE users;" | grep -iE 'locality|crdb_region' | head -4
for gw in "15440 ru-central" "15443 ru-west" "15446 ru-east"; do
    set -- $gw
    measure "запись-через-$2" "$1" geo-insert
done

echo "--- как строки распределились по регионам:"
psql_c -e "SELECT crdb_region, count(*) AS rows FROM users GROUP BY crdb_region ORDER BY 1;"

distinct_regions="$(sql_value "SELECT count(DISTINCT crdb_region) FROM users;")"
[ "$distinct_regions" -eq 3 ] \
    || fail "строки легли только в $distinct_regions регион(а) — REGIONAL BY ROW не сработал как задумано"
log "строки распределились по всем $distinct_regions регионам"

# Сверяем ПОШТУЧНО: сколько транзакций отчиталось об успехе через каждый
# шлюз против того, сколько строк оказалось в его регионе. Точного
# совпадения ждать нельзя — часть коммитов могла пройти без ответа
# клиенту, — но расхождение обязано быть в пределах доли процента,
# иначе привязка строки к шлюзу работает не так, как заявлено.
echo "--- сверка «записано через шлюз» против «строк в регионе»:"
for pair in "ru-central запись-через-ru-central" "ru-west запись-через-ru-west" "ru-east запись-через-ru-east"; do
    set -- $pair
    region="$1"; tag="$2"
    ops="$(awk -F'[:,]' '{ for (i = 1; i < NF; i++) if ($i ~ /"ops"/) { gsub(/[^0-9]/, "", $(i+1)); print $(i+1) } }' "$OUT/$tag.json")"
    rows="$(sql_value "SELECT count(*) FROM users WHERE crdb_region = '$region';")"
    diff=$(( rows - ops ))
    pct="$(awk -v d="$diff" -v o="$ops" 'BEGIN { if (o > 0) printf "%.2f", (d < 0 ? -d : d) * 100 / o; else print "н/д" }')"
    printf '  %-12s записано %-6s строк %-6s расхождение %-5s (%s%%)\n' "$region" "$ops" "$rows" "$diff" "$pct"
    awk -v p="$pct" 'BEGIN { exit (p + 0 <= 1.0) ? 0 : 1 }' \
        || log "ВНИМАНИЕ: расхождение по региону $region больше процента — разобрать"
done

echo "--- где физически лежат реплики строк каждого региона:"
psql_c -e "SELECT DISTINCT replica_localities FROM [SHOW RANGES FROM TABLE users WITH DETAILS];"

echo
echo "=== C. Survival goal: зона против региона ==="
#
# ВАЖНО: считать надо ГОЛОСУЮЩИЕ реплики, а не все. Первая версия этого
# замера брала длину массива replicas и делала вывод «пять реплик при
# обеих целях, меняется концентрация 3 -> 2». Это неверно: в кворуме
# участвуют только voting-реплики, и меняется именно их число.
#
# Измерено на живом кластере:
#   ZONE   FAILURE: num_replicas = 5, num_voters = 3,
#                   voter_constraints = '[+region=<домашний>]'
#   REGION FAILURE: num_replicas = 5, num_voters = 5,
#                   voter_constraints = '{+region=<домашний>: 2}'
#
# То есть при выживании зоны все три голосующих сидят в домашнем регионе
# и кворум 2 из 3 не покидает его; при выживании региона голосующих пять,
# больше двух в одном регионе не даётся, и кворум 3 из 5 обязан пересечь
# границу.
voters_of() { # печатает распределение «число голосующих -> сколько диапазонов»
    psql_c -e "SELECT array_length(voting_replicas, 1) AS voters,
                      coalesce(array_length(non_voting_replicas, 1), 0) AS nonvoters,
                      array_length(replicas, 1) AS all_replicas,
                      count(*) AS ranges
               FROM [SHOW RANGES FROM TABLE users WITH DETAILS]
               GROUP BY 1, 2, 3 ORDER BY 1;"
}

# Максимум ГОЛОСУЮЩИХ в одном регионе.
#
# В SQL это не посчитать в лоб: replica_localities выровнен по массиву
# replicas, а не по voting_replicas, и сопоставить их внутри запроса
# нечем. Поэтому берём карту «узел -> регион» отдельно и считаем в awk.
#
# Считаем ПОКАЗАТЕЛЬНО для каждого диапазона, а не только глобальный
# максимум. Глобального максимума мало: он отвечает на вопрос «не собралось
# ли большинство в одном регионе», но молчит о том, лежат ли голосующие в
# СВОЁМ регионе — а именно это и требует voter_constraints при цели «зона».
#
# ДОМАШНИЙ РЕГИОН ДИАПАЗОНА. Первая версия искала имя региона прямо в
# start_key — и не нашла ни разу: crdb_region это ENUM, и в ключе стоит его
# физический байт (\x80, \xa0, \xc0), а не текст. Все диапазоны получали
# «?», а проверка на этом молча проходила. Поэтому карту «байт -> регион»
# строим эмпирически: берём существующую строку известного региона,
# спрашиваем SHOW RANGE ... FOR ROW, в каком она диапазоне, и читаем байт
# из start_key этого диапазона. Порядок байтов не документирован и от
# порядка объявления регионов не зависит — угадывать его нельзя.
region_byte_map() { # байт enum'а -> регион; плюс регион ПЕРВОЙ партиции
    local reg id rid key b
    : > /tmp/crdb-05-regionmap
    : > /tmp/crdb-05-leadregion
    for reg in $(psql_c --format=tsv -e \
                 "SELECT region FROM [SHOW REGIONS FROM DATABASE geo];" | tail -n +2); do
        id="$(sql_value "SELECT id FROM users WHERE crdb_region = '$reg' LIMIT 1;" 2>/dev/null || true)"
        [ -n "$id" ] || { log "в регионе $reg нет строк — байт не определить"; continue; }
        rid="$(sql_value "SELECT range_id FROM [SHOW RANGE FROM TABLE users FOR ROW ('$reg', '$id')];" 2>/dev/null || true)"
        [ -n "$rid" ] || continue
        key="$(sql_value "SELECT start_key FROM [SHOW RANGES FROM TABLE users] WHERE range_id = $rid;" 2>/dev/null || true)"
        # Ищем «x» с двумя шестнадцатеричными цифрами, а не «\x»: в regexp
        # awk обратный слеш пришлось бы экранировать, и gawk всё равно ругался
        # бы на \x как на незавершённую escape-последовательность.
        # SIGPIPE под pipefail: сначала в переменную, потом разбор.
        b="$(printf '%s' "$key" | awk 'match($0, /x[0-9a-f][0-9a-f]/) { print substr($0, RSTART, RLENGTH); exit }')"
        if [ -n "$b" ]; then
            printf '%s\t%s\n' "$b" "$reg" >> /tmp/crdb-05-regionmap
        else
            # Байта нет — значит, диапазон этого региона начинается с начала
            # таблицы (<TableMin>), то есть регион идёт ПЕРВОЙ партицией.
            #
            # Брать байт из end_key, как делала предыдущая версия, нельзя:
            # он принадлежит уже СЛЕДУЮЩЕЙ партиции, и первый диапазон
            # получал чужой дом. Ошибка была не безобидной: раскладка
            # выглядела нарушенной там, где она в порядке.
            printf '%s\n' "$reg" >> /tmp/crdb-05-leadregion
        fi
    done
    cat /tmp/crdb-05-regionmap
    [ -s /tmp/crdb-05-leadregion ] \
        && printf '<начало таблицы>\t%s\n' "$(head -1 /tmp/crdb-05-leadregion)"
    return 0
}

range_layout() { # TSV: range_id, домашний регион, голосующих дома, максимум в одном регионе
    local map ranges
    map="$(docker exec "$PRIMARY_NODE" cockroach node status --insecure \
             --host=localhost:26257 --format=tsv 2>/dev/null \
           | awk -F'\t' 'NR > 1 { split($2, a, ":"); print $1 "\t" a[1] }')"
    # контейнер -> регион известен из имени сервиса в compose
    #
    # range_size нужен, чтобы отличить диапазоны С ДАННЫМИ от пустых
    # промежутков между партициями. Промежутки строк не содержат, требовать
    # от них раскладки по регионам не за что, а в утверждении они выглядели
    # бы нарушением: «дома ноль голосующих».
    ranges="$(psql_c --format=tsv -e \
        "SELECT range_id, array_to_string(voting_replicas, ','), start_key, range_size
         FROM [SHOW RANGES FROM TABLE users WITH DETAILS];" | tail -n +2)"

    printf '%s\n' "$map" | awk -F'\t' '
        { node_container[$1] = $2 }
        END { for (n in node_container) print n "\t" node_container[n] }
    ' > /tmp/crdb-05-nodemap

    printf '%s\n' "$ranges" | awk -F'\t' '
        BEGIN {
            OFS = "\t"
            while ((getline line < "/tmp/crdb-05-nodemap") > 0) {
                split(line, p, "\t")
                c = p[2]
                # geo1-3 -> ru-central, geo4-6 -> ru-west, geo7-9 -> ru-east.
                # Ключ здесь — ИМЯ КОНТЕЙНЕРА, а не node_id: они не совпадают.
                idx = substr(c, 4) + 0
                reg = (idx <= 3) ? "ru-central" : (idx <= 6 ? "ru-west" : "ru-east")
                node_region[p[1]] = reg
            }
            while ((getline line < "/tmp/crdb-05-regionmap") > 0) {
                split(line, q, "\t")
                byte_region[q[1]] = q[2]
            }
            lead = ""
            if ((getline line < "/tmp/crdb-05-leadregion") > 0) lead = line
            # Наименьший ИЗВЕСТНЫЙ байт. Байт самой ведущей партиции в карту
            # не попадает (её диапазон начинается с начала таблицы), поэтому
            # всё, что лежит НИЖЕ минимума известных, принадлежит ей же —
            # ключевое пространство упорядочено по этому байту.
            minb = ""
            for (k in byte_region) if (minb == "" || k < minb) minb = k
        }
        {
            n = split($2, v, ",")
            delete cnt
            for (i = 1; i <= n; i++) cnt[node_region[v[i]]]++
            mx = 0
            for (r in cnt) if (cnt[r] > mx) mx = cnt[r]
            b = ""
            if (match($3, /x[0-9a-f][0-9a-f]/)) b = substr($3, RSTART, RLENGTH)
            if (b == "") home = (lead != "" ? lead : "?")
            else if (b in byte_region) home = byte_region[b]
            else if (lead != "" && minb != "" && b < minb) home = lead
            else home = "?"
            #
            # Классифицируем по ДЕЙСТВИЮ КОНФИГУРАЦИИ, а не по наличию
            # строк. Конфигурация партиции покрывает диапазон ключей
            # независимо от того, есть ли в нём данные, и фильтр по
            # range_size мог бы спрятать неверную раскладку пустого, но
            # покрытого конфигурацией диапазона.
            #
            # Диапазон лежит ВНУТРИ партиции, если начинается с её байта и
            # не с границы «/PrefixEnd» (та стоит уже за концом партиции).
            # Первый диапазон таблицы (<TableMin>) — внутри ведущей.
            inside = "?"
            if (b == "") inside = (lead != "" ? "внутри" : "?")
            else if ($3 ~ /PrefixEnd/) inside = "граница"
            else if (home != "?") inside = "внутри"
            size = $4 + 0
            print $1, home, (home in cnt ? cnt[home] : 0), mx, inside, (size > 0 ? "данные" : "пусто")
        }
    '
}

layout_metrics() { # $1 = файл раскладки; печатает «максимум минимум_дома»
    #
    # Утверждаем по всем диапазонам ВНУТРИ партиций — независимо от того,
    # есть ли в них строки: конфигурация партиции покрывает ключевой
    # диапазон, а не строки. Отдельно печатаем, сколько диапазонов в
    # утверждение НЕ вошло: полнота проверки должна быть видна, а не
    # подразумеваться.
    #
    # За пределами партиций остаются пограничные диапазоны, начинающиеся
    # с «/PrefixEnd»: они лежат за концом партиции, её конфигурация на них
    # не распространяется, и требовать от них раскладки не за что.
    awk -F'\t' '
        $5 == "внутри" { if ($4 > mx) mx = $4 }
        $5 == "внутри" && $2 != "?" { if (!seen++ || $3 < mh) mh = $3 }
        $5 != "внутри" { outside++ }
        END { print (mx + 0), (seen ? mh : -1), (seen + 0), (outside + 0) }
    ' "$1"
}

zone_voters() { # num_voters из конфига зоны — то, что система САМА объявляет
    #
    # Раньше здесь стоял grep по трём ключам: num_replicas, num_voters,
    # voter_constraints. Из-за него строка constraints ни разу не попала в
    # поле зрения — а она распространяется на ВСЕ реплики, не только на
    # голосующие, и без неё вывод о «формальном соответствии» раскладки
    # делать нельзя. Печатаем конфиг целиком, включая партиции.
    psql_c -e "SHOW ZONE CONFIGURATION FOR TABLE users;" | sed 's/^/    /'
    local reg
    for reg in $(psql_c --format=tsv -e \
                 "SELECT region FROM [SHOW REGIONS FROM DATABASE geo];" | tail -n +2); do
        echo "    --- партиция $reg:"
        psql_c -e "SHOW ZONE CONFIGURATION FOR PARTITION \"$reg\" OF INDEX users@users_pkey;" \
            | sed 's/^/        /' || true
    done
}

psql_c -e "ALTER DATABASE geo SURVIVE ZONE FAILURE;" >/dev/null
log "ждём, пока размещение устоится…"
sleep 90
psql_c -e "SELECT survival_goal FROM [SHOW DATABASES] WHERE database_name = 'geo';"

echo "--- что объявляет сама система (цель ЗОНА):"
zone_voters
zone_nv="$(sql_value "SELECT count(*) FROM [SHOW ZONE CONFIGURATION FOR TABLE users];")"
[ "${zone_nv:-0}" -ge 1 ] || fail "конфиг зоны не читается — механику подтвердить нечем"

echo "--- фактическая раскладка по диапазонам (цель ЗОНА):"
voters_of
echo "--- по диапазонам: id, дом, голосующих дома, максимум в регионе, внутри/граница, данные:"
echo "--- карта «байт enum'а -> регион» (иначе домашний регион не определить):"
region_byte_map | sed 's/^/    /'
range_layout > /tmp/crdb-05-layout-zone
sed 's/^/    /' /tmp/crdb-05-layout-zone
read -r zone_max_voters zone_min_home zone_checked zone_skipped <<EOF
$(layout_metrics /tmp/crdb-05-layout-zone)
EOF
log "в утверждение вошло диапазонов: $zone_checked; вне партиций (не проверялись): $zone_skipped"
log "максимум ГОЛОСУЮЩИХ в одном регионе при выживании зоны: $zone_max_voters"
log "минимум голосующих В СВОЁМ регионе по всем диапазонам: $zone_min_home"
#
# Утверждение по КАЖДОМУ диапазону, а не по глобальному максимуму. Иначе
# один удачно разложенный диапазон закрывал бы собой все остальные:
# максимум 3 достижим и тогда, когда у прочих партиций дома нет ни одного
# голосующего. Требование voter_constraints адресовано каждой партиции.
if [ "${zone_min_home:--1}" -lt 0 ]; then
    #
    # Отдельная ветка, а не «меньше трёх». Неопределённый дом — это НЕ
    # нарушение раскладки, это несостоявшаяся проверка, и подавать её как
    # найденный дефект нельзя: ровно так проверка отсутствия и проходит
    # на пустых данных.
    log "ВНИМАНИЕ: домашний регион диапазонов не определён — проверка по"
    log "диапазонам НЕ ВЫПОЛНЕНА. Смотреть карту байтов выше, а не выводы."
elif [ "$zone_min_home" -lt 3 ]; then
    log "ВНИМАНИЕ: есть диапазоны, у которых дома меньше трёх голосующих"
    log "(минимум $zone_min_home). Глобальный максимум это скрывает."
else
    log "у каждого диапазона в родном регионе не меньше трёх голосующих"
fi

#
# ДИАГНОСТИКА. При цели «зона» конфигурация требует ВСЕ три голосующих в
# родном регионе партиции, то есть максимум обязан быть равен 3. Если он
# меньше — размещение не соответствует объявленному, и это надо не
# списывать на «маленькую таблицу», а показать сигналами кластера.
if [ "${zone_max_voters:-0}" -lt 3 ]; then
    log "ВНИМАНИЕ: раскладка НЕ соответствует voter_constraints (ожидалось 3 в родном регионе)."
    log "Собираем диагностику, а не объясняем задним числом."

    echo "--- сигналы очереди репликации по узлам (только ненулевые):"
    sig="$(for p in 18090 18091 18092 18093 18094 18095 18096 18097 18098; do
             curl -sf --max-time 5 "http://127.0.0.1:${p}/_status/vars" 2>/dev/null                | awk '/^ranges_underreplicated|^ranges_overreplicated|^queue_replicate_pending|^queue_replicate_process_failure|^queue_replicate_addreplica_error|^queue_replicate_purgatory/ { print "    " $1 " " $2 }'                | grep -vE ' 0$' || true
           done)"
    if [ -n "$sig" ]; then
        printf '%s
' "$sig"
        log "очередь репликации ОШИБАЕТСЯ — значит, дело не в «нечего оптимизировать»,"
        log "а в том, что аллокатор не может выполнить объявленное размещение."
        echo "--- причина отказов (из файловых логов узла, не из docker logs):"
        # docker logs у CockroachDB почти пуст: он пишет в файлы внутри
        # cockroach-data/logs. Ищем сообщения очереди репликации там.
        for n in geo1 geo9; do
            docker exec "$n" sh -c                 "grep -ihE 'queue processing resulted|throttled|suspect|unable to allocate'                  /cockroach/cockroach-data/logs/cockroach.log 2>/dev/null | tail -2"                 2>/dev/null | cut -c1-200 | sed "s/^/    [$n] /" || true
        done
    else
        log "очередь репликации пуста и ошибок не показывает"
    fi

    echo "--- сколько диапазонов не добрали реплик до целевых пяти:"
    psql_c -e "SELECT array_length(replicas, 1) AS replicas, count(*) AS ranges
               FROM [SHOW RANGES FROM TABLE users WITH DETAILS] GROUP BY 1 ORDER BY 1;"

    echo "--- живы ли все девять узлов и сколько на каждом реплик:"
    psql_c -e "SELECT node_id, count(*) AS replicas
               FROM (SELECT unnest(replicas) AS node_id FROM [SHOW CLUSTER RANGES])
               GROUP BY node_id ORDER BY node_id;"

    log "если очередь пуста, ошибок нет, узлы живы, а раскладка всё равно"
    log "не сходится — это и есть результат: объявленная конфигурация зоны"
    log "описывает намерение, а не достигнутое состояние. Так и записывать."
elif [ "${zone_min_home:--1}" -ge 3 ]; then
    log "раскладка соответствует объявленному: у каждого диапазона С ДАННЫМИ"
    log "три голосующих в родном регионе"
else
    #
    # Раньше здесь стояло безусловное «соответствует». Это было неверно:
    # глобального максимума 3 достаточно, чтобы ОДИН диапазон разложился
    # правильно, а остальные — нет. Именно так и вышло на прогоне: у
    # диапазона основного региона дома трое, у прочих — двое и ноль.
    log "ВНИМАНИЕ: глобальный максимум достигнут, но НЕ у всех диапазонов:"
    log "минимум голосующих дома — $zone_min_home. Соответствием это не считается."
fi

measure "zone-запись-ru-central" 15440 geo-insert

psql_c -e "ALTER DATABASE geo SURVIVE REGION FAILURE;" >/dev/null
log "ждём перераспределения (число голосующих должно вырасти до пяти)…"
settled=0
for _ in $(seq 1 90); do
    left="$(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE users WITH DETAILS]
                       WHERE array_length(voting_replicas, 1) < 5;" 2>/dev/null || echo "")"
    case "$left" in
        ''|*[!0-9]*) sleep 10; continue ;;
    esac
    if [ "$left" -eq 0 ]; then settled=1; break; fi
    sleep 10
done

psql_c -e "SELECT survival_goal FROM [SHOW DATABASES] WHERE database_name = 'geo';"
echo "--- что объявляет сама система (цель РЕГИОН):"
zone_voters
echo "--- фактическая раскладка по диапазонам (цель РЕГИОН):"
voters_of
echo "--- по диапазонам: id, дом, голосующих дома, максимум в регионе, внутри/граница, данные:"
range_layout > /tmp/crdb-05-layout-region
sed 's/^/    /' /tmp/crdb-05-layout-region
read -r region_max_voters region_min_home region_checked region_skipped <<EOF
$(layout_metrics /tmp/crdb-05-layout-region)
EOF
log "в утверждение вошло диапазонов: $region_checked; вне партиций (не проверялись): $region_skipped"
log "максимум ГОЛОСУЮЩИХ в одном регионе при выживании региона: $region_max_voters"
log "минимум голосующих В СВОЁМ регионе по всем диапазонам: $region_min_home"
#
# Ключевое утверждение этой цели: больше двух голосующих в одном регионе
# не даётся НИ ОДНОМУ диапазону, поэтому кворум 3 из 5 обязан пересечь
# границу. Без этой проверки фаза «регион» вообще ничего не утверждала:
# число печаталось, но никак не сверялось с voter_constraints.
if [ "${region_max_voters:-0}" -gt 2 ]; then
    log "ВНИМАНИЕ: у какого-то диапазона в одном регионе $region_max_voters голосующих"
    log "при ограничении 2 — кворум мог бы собраться внутри региона, и тогда"
    log "заявленная выживаемость региона НЕ обеспечена. Разбирать, а не округлять."
elif [ "$settled" -eq 1 ]; then
    log "ограничение соблюдено: ни в одном регионе больше двух голосующих нет,"
    log "то есть кворум 3 из 5 не может собраться внутри одного региона"
fi

# Сходимость на этом стенде достигается не всегда: партиции без строк
# аллокатор доводит до целевого числа реплик неохотно. Это НЕ повод
# объявлять замер удавшимся или проваленным молча — фиксируем как есть.
if [ "$settled" -eq 1 ]; then
    log "все диапазоны получили по пять голосующих"
else
    log "ВНИМАНИЕ: не все диапазоны дошли до пяти голосующих (осталось: ${left:-?})."
    log "Вывод про механику опирается на конфиг зоны выше, а раскладка —"
    log "на сошедшиеся диапазоны. В FIXTURES это записывается отдельно."
fi

echo "--- голосующие одного сошедшегося диапазона и локальности всех реплик:"
psql_c -e "SELECT range_id, voting_replicas, replica_localities
           FROM [SHOW RANGES FROM TABLE users WITH DETAILS]
           WHERE array_length(voting_replicas, 1) = 5 LIMIT 1;"

measure "region-запись-ru-central" 15440 geo-insert

echo
echo "=== D. Latency через разные шлюзы (ПОЛ величин, без географии) ==="
log "напоминание: расстояния между узлами нет, разница здесь — не цена географии"
for gw in "15440 ru-central" "15446 ru-east"; do
    set -- $gw
    measure "чтение-через-$2" "$1" geo-read
done

echo "--- обычное чтение против follower read (шлюз ru-east, 5 повторов):"
for _ in 1 2 3 4 5; do
    docker exec -i geo7 cockroach sql --insecure --host=localhost:26257 --database=geo \
        -e "EXPLAIN ANALYZE SELECT count(*) FROM users;" \
        | grep -iE 'execution time' | head -1 | sed 's/^/    обычное:  /'
done
for _ in 1 2 3 4 5; do
    docker exec -i geo7 cockroach sql --insecure --host=localhost:26257 --database=geo \
        -e "EXPLAIN ANALYZE SELECT count(*) FROM users AS OF SYSTEM TIME follower_read_timestamp();" \
        | grep -iE 'execution time' | head -1 | sed 's/^/    follower: /'
done

echo
echo "=== Постусловие ==="
rows="$(sql_value "SELECT count(*) FROM users;")"
log "строк в users: $rows"
[ "$rows" -gt 0 ] || fail "в таблицу ничего не записалось — замеры несостоятельны"
psql_c -e "SELECT crdb_region, count(*) AS rows FROM users GROUP BY crdb_region ORDER BY 1;"
