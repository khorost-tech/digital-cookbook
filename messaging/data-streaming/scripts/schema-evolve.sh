#!/usr/bin/env bash
# Эволюция схемы: в payload добавляется новое поле (currency), которого не
# было при разработке остальных звеньев. Проверяем СКВОЗНОЕ и ФАЛЬСИФИЦИРУЕМОЕ
# свойство — не "процесс не упал" (это тавтология не крепче, чем FINAL=5 при
# 5 клиентах — см. брифинг задачи), а "новое событие реально дошло до витрины
# невредимым, а сумма в ClickHouse по-прежнему сходится с PG до цента".
#
# Разбор по звеньям — какие ДЕЙСТВИТЕЛЬНО не знают о currency, а какие нет:
#
#  1) Debezium/EventRouter (connect/debezium-outbox.json) — currency доезжает
#     до Kafka; ниже это ПОКАЗАНО прогоном: событие с новым полем читается из
#     топика orders.events сырым.
#     Причина — на уровне конфигурации, одной фразой: при
#     transforms.outbox.table.expand.json.payload=true роутер выводит схему из
#     набора ключей на КАЖДОЙ записи (per-record-инференс, без сверки с ранее
#     зафиксированной версией), а сериализация идёт с
#     value.converter.schemas.enable=false.
#     Глубже (внутренности Debezium, WAL, логическая репликация) — тема
#     соседней серии (wal-and-analogs), не этой: эта серия владеет сборкой и
#     швами между звеньями. Из пределов инференса читателю достаточно знать:
#     он не всеяден — разнотипные элементы одного массива и пустой массив его
#     валят; currency этого предела не задевает, и мы на разбор чужого кода
#     не опираемся — ниже читаем реальное событие из топика.
#
#  2) Kafka Streams (streams/src/main/java/StreamsApp.java, parseEvent())
#     читает orders.events через Jackson JsonNode.get("customer_id") /
#     .get("amount") — tree-API без схемы, без FAIL_ON_UNKNOWN_PROPERTIES.
#     Лишние ключи просто не запрашиваются. Это звено ТОЖЕ действительно не
#     знает о currency и не может сломаться на нём — заведомо, по коду, а не
#     случайно. Проверяем это ниже: агрегат для клиента после нового события
#     считаем НЕЗАВИСИМО через PG и сверяем с тем, что реально посчитал
#     Streams, — до ClickHouse и до Go-консьюмера.
#
#  3) НО: TotalsProcessor.process() (тот же файл) строит выходной JSON
#     ЗАНОВО из трёх полей (customer_id/orders/total) через ObjectNode —
#     currency в customer.totals не попадает НИКОГДА, вне зависимости от
#     того, ломается Streams на новом поле или нет. Значит Go-консьюмер
#     (consumer/main.go, читает только customer.totals) физически не может
#     "увидеть" currency: поле до него не доезжает ни при каких условиях.
#     Заявлять "Go-консьюмер игнорирует новое поле" было бы тавтологией
#     ровно того класса, о котором предупреждает брифинг задачи (свойство
#     нечем сломать — поле никогда не встречается на входе). Ниже это прямо
#     проверяется (ассерт "currency НЕ в customer.totals") и честно
#     проговаривается в итоговой сводке, а не замалчивается.
#
#     Содержательная замена для Go-консьюмера на РЕАЛЬНОМ трафике: его
#     input-схема не менялась вообще, он как ни в чём не бывало разбирает
#     штатный customer.totals, и сумма витрины после его прогона по-прежнему
#     сходится с PG до цента — то есть новое поле нигде по дороге не
#     потеряло и не исказило данные.
#
#     ДОПОЛНИТЕЛЬНО (ревью Important-4): устойчивость json.Unmarshal Go к
#     незнакомым полям — заявление, которое реальный трафик здесь доказать
#     не может (currency до consumer'а физически не доезжает, см. п.3).
#     Первая редакция этого файла отказывалась проверять это вообще, "чтобы
#     не выдавать синтетику за наблюдение с работающего стенда" — но стенд
#     уже содержит принятый прецедент именно такой синтетики (inject-bad.sh,
#     Задача 5: заведомо битое сообщение, вброшенное в живой customer.totals
#     напрямую). Непоследовательно отвергать синтетику принципом, который
#     стенд сам не соблюдает. Поэтому ниже (шаг 5.1) — узкий, ИЗОЛИРОВАННЫЙ
#     синтетический пробник: одно сообщение с ключом $SYNTH_CUSTOMER_ID
#     (заведомо вне диапазона реальных customer_id 1..5, поэтому не может
#     исказить ни один PG-сверяемый агрегат) и лишним полем, отправленное
#     ПРЯМО в customer.totals в обход Streams. Оно проходит через ТОТ ЖЕ
#     реальный Go-консьюмер и оседает в ТОМ ЖЕ реальном ClickHouse — не
#     мок. Фальсифицируемо: если бы consumer/main.go:186 (json.Unmarshal)
#     использовал json.NewDecoder(...).DisallowUnknownFields() вместо
#     обычного json.Unmarshal, эта запись ушла бы в DLQ (битых>0), и шаг 5.1
#     упал бы. Изоляция через синтетический
#     customer_id — то же самое инженерное решение, каким seed.sh/inject-bad.sh
#     уже используют выделенные диапазоны id, чтобы не путать демо-данные
#     со служебными.
#
# Технические ловушки, обойдённые ниже (проверено живьём, не предположение):
#  - "--offset -1" на этой версии Kafka CLI ОТВЕРГАЕТСЯ: "provided offset
#    value '-1' is incorrect. Valid values are 'earliest', 'latest', or a
#    non-negative long" (см. отчёт: воспроизведено `kafka-console-consumer.sh
#    ... --offset -1`, RC=1). Черновик задачи (Step 2 в брифинге) использует
#    именно "--offset -1" — это НЕ воспроизводится на этом брокере, здесь не
#    используется. "--offset latest" тоже не подходит: это "жди новых
#    сообщений с этого момента", а не "прочти уже существующее последнее" —
#    гонка, если продюсер успевает записать раньше, чем стартует консьюмер.
#    Вместо этого используется техника "забора": оффсет конца партиции
#    фиксируется ДО вставки (kafka-get-offsets.sh), новые записи читаются
#    явно НАЧИНАЯ с этого оффсета.
#  - kafka.tools.GetOffsetShell удалён в Kafka 4.x — везде kafka-get-offsets.sh.
#  - orders.events — 3 партиции (murmur2 по order_id); новое событие может
#    попасть в любую из трёх, а не в partition 0 (как в черновике брифинга) —
#    ищем во всех трёх.
#  - docker exec/psql/go run — без "out=$(cmd)" напрямую под set -e (такое
#    присваивание при ошибке команды убивает скрипт ДО печати диагностики,
#    см. lag.sh). Везде "if ! out=$(cmd 2>&1); then echo ОШИБКА: $out; exit 1;
#    fi" — кроме мест, где ненулевой код ОЖИДАЕМ и не является отказом
#    (--timeout-ms консьюмера без сообщений в конкретной партиции): там explicit
#    "|| true" и решение принимается по СОДЕРЖИМОМУ вывода, а не по коду
#    возврата, а при итоговой неудаче печатается вся собранная диагностика.
#  - ПРЕДУСЛОВИЕ (ревью круга 2, Minor-3): проверка "битых=0/dlq_ошибок=0"
#    после запуска Go-консьюмера (шаг 6, ниже) проходит ТОЛЬКО если битое
#    сообщение из демонстрации Задачи 5 (inject-bad.sh) на этом стенде уже
#    вычитано консьюмером (тот демо-прогон Задачи 5 доехал до конца, битое
#    сообщение подтверждённо ушло в DLQ). Если нет — этот скрипт своим
#    "go run . -for 15s" (без -from-start) наткнётся на тот же непрочитанный
#    непарсимый JSON и упадёт на bad!=0 здесь, ПОСЛЕ уже необратимой вставки
#    нового заказа в PG (шаг 2) и после отправки синтетического пробника
#    (шаг 5.1) — то есть по причине, не имеющей отношения к currency.
#    На Задачу 8: демо Задачи 5 (inject-bad.sh + консьюмер) должно
#    отработать до конца ДО schema-evolve.sh, не после и не вперемешку.
#  - НЕОБРАТИМО (тот же стандарт раскрытия, что и в inject-bad.sh, Minor-4):
#    каждый запуск этого скрипта навсегда добавляет один заказ в PG
#    (customer_id=$CUSTOMER_ID, см. шаг 2) и одну фантомную строку
#    customer_id=$SYNTH_CUSTOMER_ID в customer_totals (см. шаг 5.1, п.7.3
#    отчёта) — обе записи невозможно откатить без docker compose down -v
#    (см. ограничение "не разрушай стенд" в задаче).
set -euo pipefail

CUSTOMER_ID=3
AMOUNT=42.00
CURRENCY=RUB
BOOTSTRAP=localhost:9092   # выполняется через docker exec ds-kafka — localhost внутри контейнера

# SYNTH_CUSTOMER_ID — заведомо вне диапазона реальных demo-клиентов (1..5,
# см. seed.sh) и вне диапазона тестовых заказов инцидента из отчёта Задачи 7
# (id 251/252 — те принадлежат customer_id=3). Используется ТОЛЬКО в шаге
# 5.1 (синтетический пробник Go-консьюмера, см. разбор Important-4 в шапке
# файла) — изоляция гарантирует, что пробник не попадёт ни в один
# PG-сверяемый агрегат (SELECT ... WHERE customer_id=$CUSTOMER_ID и
# итоговые PG-суммы по orders его не видят вообще, он существует только в
# Kafka/ClickHouse).
SYNTH_CUSTOMER_ID=999

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
consumer_dir="$(cd "$script_dir/../consumer" && pwd)"
streams_dir="$(cd "$script_dir/../streams" && pwd)"

# find_streams_pid — pgrep -f 'data-streaming/streams' ловит ЛЮБОЙ процесс,
# чья ПОЛНАЯ КОМАНДНАЯ СТРОКА содержит эту подстроку, не только сам процесс
# Kafka Streams (проверено живьём: обёртка вида `bash -lc '... data-streaming/streams ...'`,
# которой запускались диагностические команды при ревью Задачи 7,
# сама себя ловила через pgrep -f). `| head -1` при этом произвольно берёт
# МЛАДШИЙ pid, а не искомый. Вместо этого ищем среди процессов, у которых в
# командной строке есть "exec:java" (запуск через exec-maven-plugin), тот,
# чей АКТУАЛЬНЫЙ working directory (/proc/$pid/cwd) — именно streams/
# этого стенда, а не любой другой каталог, где подстрока просто встретилась.
find_streams_pid() {
  local pid cwd
  for pid in $(pgrep -f 'exec:java' 2>/dev/null || true); do
    cwd="$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)"
    if [[ "$cwd" == "$streams_dir" ]]; then
      printf '%s\n' "$pid"
      return 0
    fi
  done
  return 1
}

# --- 0. Предусловие: Kafka Streams должен уже работать ---------------------
# schema-evolve.sh не поднимает и не перезапускает звенья — как и остальные
# скрипты стенда (seed.sh, inject-bad.sh, lag.sh), он предполагает, что
# `docker compose up -d && create-topics.sh && curl .../connectors` (см.
# README.md) уже отработали, и ОТДЕЛЬНО, вручную, поднят Kafka Streams
# (`cd streams && KAFKA=localhost:9096 mvn -q compile exec:java`) — README
# этого шага не документирует (пробел, унаследованный от предыдущих задач
# стенда, не создаётся заново этим скриптом). Без него схема эволюционирует
# только до Kafka, но не до витрины — фиксируем это явной проверкой, а не
# тихим "ничего не произошло" через 20 секунд ожидания.
#
# KAFKA=host:port (а не -DKAFKA=...): StreamsApp.env() читает
# System.getenv("KAFKA") (переменную окружения), а exec-maven-plugin 3.5.0
# НЕ мапит `-D` в окружение запускаемого main — `-DKAFKA=...` был бы молча
# проигнорирован (совпадал бы с дефолтом localhost:9096 только случайно).
streams_pid="$(find_streams_pid || true)"
if [[ -z "$streams_pid" ]]; then
  echo "ОШИБКА: процесс Kafka Streams (mvn exec:java, streams/) не найден. Подними его отдельно:" >&2
  echo "  cd streams && KAFKA=localhost:9096 mvn -q compile exec:java" >&2
  exit 1
fi
streams_start="$(ps -o lstart= -p "$streams_pid" 2>/dev/null || true)"
echo "Kafka Streams: pid=$streams_pid, запущен с: $streams_start"

# --- 1. Оффсеты-"заборы" ДО вставки ----------------------------------------
if ! oe_before_raw="$(docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh \
  --bootstrap-server "$BOOTSTRAP" --topic orders.events 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать оффсеты orders.events: $oe_before_raw" >&2
  exit 1
fi
declare -A oe_before
while IFS=: read -r _ part off; do
  [[ -n "$part" ]] && oe_before[$part]="$off"
done <<<"$oe_before_raw"
echo "orders.events до вставки: p0=${oe_before[0]:-?} p1=${oe_before[1]:-?} p2=${oe_before[2]:-?}"

if ! ct_before_raw="$(docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh \
  --bootstrap-server "$BOOTSTRAP" --topic customer.totals --partitions 0 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать оффсет customer.totals: $ct_before_raw" >&2
  exit 1
fi
ct_before="${ct_before_raw##*:}"
echo "customer.totals до вставки: offset=$ct_before"

if ! dlq_before_raw="$(docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh \
  --bootstrap-server "$BOOTSTRAP" --topic customer.totals.dlq --partitions 0 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать оффсет customer.totals.dlq: $dlq_before_raw" >&2
  exit 1
fi
dlq_before="${dlq_before_raw##*:}"
echo "customer.totals.dlq до вставки: offset=$dlq_before"

# --- 2. Вставляем заказ с НОВЫМ полем currency в payload -------------------
# Один SQL-оператор (WITH ... INSERT ... SELECT ... RETURNING) — заказ и
# событие outbox пишутся одной атомарной командой (тот же смысл, что и в
# seed.sh: событие не может разойтись с бизнес-данными), явный BEGIN/COMMIT
# не нужен. RETURNING отдаёт id нового заказа для дальнейшей проверки.
if ! insert_raw="$(docker exec -i ds-postgres psql -U shop -d shop -t -A -v ON_ERROR_STOP=1 <<SQL 2>&1
WITH new_order AS (
  INSERT INTO orders (customer_id, amount) VALUES ($CUSTOMER_ID, $AMOUNT) RETURNING id
)
INSERT INTO outbox (aggregatetype, aggregateid, type, payload)
SELECT 'orders', id::text, 'OrderCreated',
       jsonb_build_object('order_id', id, 'customer_id', $CUSTOMER_ID,
                           'amount', $AMOUNT, 'currency', '$CURRENCY')
FROM new_order
RETURNING (payload->>'order_id')::bigint;
SQL
)"; then
  echo "ОШИБКА: вставка заказа с currency провалилась: $insert_raw" >&2
  exit 1
fi
oid="$(printf '%s\n' "$insert_raw" | grep -E '^[0-9]+$' | head -1 || true)"
if [[ -z "$oid" ]]; then
  echo "ОШИБКА: не удалось извлечь id нового заказа из вывода psql: $insert_raw" >&2
  exit 1
fi
echo "событие с новым полем отправлено: order_id=$oid customer_id=$CUSTOMER_ID amount=$AMOUNT currency=$CURRENCY"

if ! pg_cust_raw="$(docker exec ds-postgres psql -U shop -d shop -t -A -c \
  "SELECT count(*), sum(amount) FROM orders WHERE customer_id=$CUSTOMER_ID;" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать PG для customer_id=$CUSTOMER_ID: $pg_cust_raw" >&2
  exit 1
fi
IFS='|' read -r pg_cust_count pg_cust_sum <<<"$pg_cust_raw"
echo "PG (истина, независимо от Kafka): у customer_id=$CUSTOMER_ID теперь orders=$pg_cust_count total=$pg_cust_sum"

# --- 3. Debezium: сырое событие в orders.events содержит currency КАК ЕСТЬ -
# Цикл идёт ПО КЛЮЧАМ oe_before (реально существующим партициям), а не по
# литералу "0 1 2": под set -u жёстко перечисленный диапазон падает БЕЗ
# диагностики, если orders.events когда-либо автосоздался с иным числом
# партиций (напр. 1, как в ровно том отказе, о котором предупреждает
# README) — oe_before[1]/[2] тогда просто нет, и "${oe_before[$p]}" убивает
# скрипт с "unbound variable" ПОСЛЕ уже необратимой вставки заказа выше.
# Воспроизведено: declare -A a; a[0]=96; for p in 0 1 2; do start="${a[$p]}"; done
# → "a[1]: unbound variable", RC=1.
#
# --max-messages 50 (было 5, ревью Minor-3): при бэклоге в партиции больше
# окна демо-событие могло не попасть в выборку и выборка нашла бы устаревшую
# запись — ложный exit 1 "не найдено". Больше окно снижает риск, не убирает
# его полностью (по-прежнему конечное число); полное решение — читать до
# LOG-END-OFFSET, а не по --max-messages, оставлено за рамками этого фикса.
declare -A poll_oe
oe_found_partition=""
for p in "${!oe_before[@]}"; do
  start="${oe_before[$p]}"
  poll_oe[$p]="$(docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh \
    --bootstrap-server "$BOOTSTRAP" --topic orders.events \
    --partition "$p" --offset "$start" --max-messages 50 --timeout-ms 20000 2>&1 || true)"
  if printf '%s\n' "${poll_oe[$p]}" | grep -qP "\"order_id\"\s*:\s*$oid\b"; then
    oe_found_partition="$p"
    break
  fi
done
if [[ -z "$oe_found_partition" ]]; then
  echo "ОШИБКА: событие order_id=$oid не найдено ни в одной партиции orders.events за 20с. Диагностика:" >&2
  for p in "${!oe_before[@]}"; do
    echo "--- partition $p (искали начиная с оффсета ${oe_before[$p]}) ---" >&2
    printf '%s\n' "${poll_oe[$p]}" >&2
  done
  exit 1
fi
raw_line="$(printf '%s\n' "${poll_oe[$oe_found_partition]}" | grep -P "\"order_id\"\s*:\s*$oid\b")"
echo "--- сырое событие в orders.events (партиция $oe_found_partition) ---"
echo "$raw_line"
if ! printf '%s' "$raw_line" | grep -qP "\"currency\"\s*:\s*\"$CURRENCY\""; then
  echo "ОШИБКА: currency отсутствует в сыром событии orders.events — Debezium не передал поле как ожидалось" >&2
  exit 1
fi
echo "ПРОВЕРЕНО: Debezium передал currency=$CURRENCY в orders.events дословно (роутер сконфигурирован expand.json.payload=true + schemas.enable=false, см. разбор в шапке файла)"

# --- 4. Kafka Streams всё ещё жив (тот же процесс, не REPLACE_THREAD-краш-луп на новом поле) ---
streams_pid_after="$(find_streams_pid || true)"
if [[ "$streams_pid_after" != "$streams_pid" ]]; then
  echo "ОШИБКА: процесс Kafka Streams изменился (было pid=$streams_pid, стало pid=${streams_pid_after:-нет}) — похоже на падение" >&2
  exit 1
fi
echo "ПРОВЕРЕНО: процесс Kafka Streams не менялся (pid=$streams_pid_after) — но это вспомогательная проверка, не главное доказательство (см. п.5: главное — правильно посчитанный агрегат)"

# --- 5. Главная проверка Kafka-уровня: агрегат в customer.totals СОВПАДАЕТ с PG,
# посчитан ДО ClickHouse и ДО Go-консьюмера — то есть именно Streams не сломался
# и не исказил число на новом поле. Falsifiable: если бы Streams споткнулся о
# currency, здесь либо не появилось бы новой записи вовсе, либо появилось бы
# устаревшее/усечённое число — сравнение с независимо посчитанным PG это ловит.
ct_out="$(docker exec ds-kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server "$BOOTSTRAP" --topic customer.totals \
  --partition 0 --offset "$ct_before" --max-messages 50 --timeout-ms 20000 2>&1 || true)"
ct_matches="$(printf '%s\n' "$ct_out" | grep -P "\"customer_id\"\s*:\s*$CUSTOMER_ID\b" || true)"
if [[ -z "$ct_matches" ]]; then
  echo "ОШИБКА: в customer.totals не появилось новой агрегации для customer_id=$CUSTOMER_ID. Сырой вывод:" >&2
  printf '%s\n' "$ct_out" >&2
  exit 1
fi
ct_last_line="$(printf '%s\n' "$ct_matches" | tail -1)"
echo "--- новая агрегация в customer.totals (посчитана Kafka Streams, ДО ClickHouse) ---"
echo "$ct_last_line"

if printf '%s' "$ct_last_line" | grep -qP '"currency"'; then
  echo "ОШИБКА: currency неожиданно попал в customer.totals — противоречит коду TotalsProcessor.process() (StreamsApp.java), который строит JSON заново только из customer_id/orders/total" >&2
  exit 1
fi
echo "ПРОВЕРЕНО: currency НЕ попал в customer.totals — TotalsProcessor пересобирает JSON заново (customer_id/orders/total), поле обрезается ЗДЕСЬ. Значит Go-консьюмер ниже физически не может увидеть currency — это не его заслуга/устойчивость, а факт топологии; см. разбор в шапке файла."

ct_orders="$(printf '%s' "$ct_last_line" | grep -oP '"orders"\s*:\s*\K[0-9]+')"
ct_total="$(printf '%s' "$ct_last_line" | grep -oP '"total"\s*:\s*\K[0-9.]+')"
echo "Streams (Kafka-уровень, независимо от ClickHouse): orders=$ct_orders total=$ct_total"

if [[ "$ct_orders" != "$pg_cust_count" ]]; then
  echo "ОШИБКА: orders в customer.totals ($ct_orders) не совпадает с PG ($pg_cust_count) для customer_id=$CUSTOMER_ID" >&2
  exit 1
fi
if ! awk -v a="$ct_total" -v b="$pg_cust_sum" 'BEGIN{d=a-b; if (d<0) d=-d; exit !(d<0.005)}'; then
  echo "ОШИБКА: total в customer.totals ($ct_total) не совпадает с PG ($pg_cust_sum) для customer_id=$CUSTOMER_ID до цента" >&2
  exit 1
fi
echo "ПРОВЕРЕНО (Kafka-уровень, ДО ClickHouse, falsifiable): Streams независимо посчитал orders=$ct_orders total=$ct_total — совпадает с PG ($pg_cust_count/$pg_cust_sum) до цента. Новое поле не сломало и не исказило агрегацию."

# --- 5.1. Синтетический пробник устойчивости Go-консьюмера (ревью Important-4) -
# currency НИКОГДА не доезжает до customer.totals через штатный конвейер (см.
# п.5 — TotalsProcessor обрезает его), поэтому "Go-консьюмер стерпел новое
# поле" реальным трафиком не проверить. Первая редакция скрипта на этом
# основании вообще отказывалась от синтетики — но стенд уже содержит
# принятый прецедент (inject-bad.sh, Задача 5), так что отказ был
# непоследовательным. Здесь — узкий и ИЗОЛИРОВАННЫЙ пробник: одно сообщение
# с ключом $SYNTH_CUSTOMER_ID (вне диапазона реальных customer_id 1..5, не
# существует в PG — не может исказить ни один PG-сверяемый агрегат) и
# лишним полем currency, отправленное ПРЯМО в customer.totals в обход
# Streams. Проходит через тот же реальный запуск Go-консьюмера (шаг 6) и
# оседает в том же реальном ClickHouse — не мок. Falsifiable: если бы
# consumer/main.go:186 (json.Unmarshal) использовал
# json.NewDecoder(...).DisallowUnknownFields(), эта запись ушла бы в DLQ
# (проверка bad=0/DLQ ниже упала бы); если бы её не подхватил сам
# консьюмер — не появилась бы в ClickHouse (проверка после шага 6).
synth_payload="{\"customer_id\":$SYNTH_CUSTOMER_ID,\"orders\":1,\"total\":9.99,\"currency\":\"$CURRENCY\"}"
if ! synth_out="$(printf '%s:%s\n' "$SYNTH_CUSTOMER_ID" "$synth_payload" | docker exec -i ds-kafka /opt/kafka/bin/kafka-console-producer.sh \
  --bootstrap-server "$BOOTSTRAP" --topic customer.totals \
  --property parse.key=true --property key.separator=: 2>&1)"; then
  echo "ОШИБКА: не удалось отправить синтетический пробник в customer.totals: $synth_out" >&2
  exit 1
fi
echo "синтетический пробник отправлен напрямую в customer.totals (минуя Streams): customer_id=$SYNTH_CUSTOMER_ID orders=1 total=9.99 + лишнее поле currency=$CURRENCY"

# --- 6. Сквозной уровень: гоним накопленный backlog (в т.ч. наше новое событие)
# через Go-консьюмер в ClickHouse и сверяем ФИНАЛЬНУЮ сумму витрины с PG.
if ! ch_before_raw="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT count(), sum(total) FROM customer_totals FINAL" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать ClickHouse customer_totals до консьюмера: $ch_before_raw" >&2
  exit 1
fi
echo "CH customer_totals FINAL до консьюмера: $ch_before_raw"

if ! raw_before="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT count() FROM raw_totals" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать ClickHouse raw_totals до консьюмера: $raw_before" >&2
  exit 1
fi
echo "CH raw_totals до консьюмера: count=$raw_before"

echo "--- запускаем Go-консьюмер (разовый прогон, не демон): customer.totals -> ClickHouse ---"
if ! consumer_out="$(cd "$consumer_dir" && GOPROXY=https://go.khorost.tech,direct go run . -for 15s 2>&1)"; then
  echo "ОШИБКА: Go-консьюмер завершился с ненулевым кодом:" >&2
  printf '%s\n' "$consumer_out" >&2
  exit 1
fi
printf '%s\n' "$consumer_out"

summary_line="$(printf '%s\n' "$consumer_out" | grep -oP 'консьюмер: вставлено=\d+ битых=\d+ dlq_ошибок=\d+ ошибок_вставки=\d+ ошибок_fetch=\d+' | head -1 || true)"
if [[ -z "$summary_line" ]]; then
  echo "ОШИБКА: не нашли строку сводки в выводе Go-консьюмера — см. вывод выше" >&2
  exit 1
fi
inserted="$(printf '%s' "$summary_line" | grep -oP 'вставлено=\K[0-9]+')"
bad="$(printf '%s' "$summary_line" | grep -oP 'битых=\K[0-9]+')"
dlq_errs="$(printf '%s' "$summary_line" | grep -oP 'dlq_ошибок=\K[0-9]+')"
if [[ "$bad" != "0" || "$dlq_errs" != "0" ]]; then
  echo "ОШИБКА: Go-консьюмер сообщил о битых записях (битых=$bad, dlq_ошибок=$dlq_errs) — в этом сценарии их быть не должно (currency до consumer'а не доезжает)" >&2
  exit 1
fi
echo "Go-консьюмер: вставлено=$inserted битых=$bad — штатный формат totals не изменился, консьюмер не заметил эволюцию схемы (потому что физически её не видит, см. п.5); синтетический пробник (см. п.5.1) в этой сводке тоже НЕ битый — проверяется ниже отдельно, что он реально дошёл до ClickHouse без искажений."

# ch_after_raw — БЕЗ синтетического пробника (customer_id=$SYNTH_CUSTOMER_ID):
# он не существует в PG и не должен участвовать в сверке суммы с PG. Его
# наличие/корректность проверяются отдельно ниже, count() FINAL без фильтра
# сравнивается с (реальные клиенты + 1) explicit.
if ! ch_after_raw="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT count(), sum(total) FROM customer_totals FINAL WHERE customer_id != $SYNTH_CUSTOMER_ID" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать ClickHouse customer_totals после консьюмера: $ch_after_raw" >&2
  exit 1
fi
IFS=$'\t' read -r ch_count ch_sum <<<"$ch_after_raw"
echo "CH customer_totals FINAL после консьюмера (реальные клиенты, без синтетического пробника): count=$ch_count sum=$ch_sum"

if ! ch_total_rows="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT count() FROM customer_totals FINAL" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать общее число строк ClickHouse customer_totals (включая синтетический пробник): $ch_total_rows" >&2
  exit 1
fi

if ! pg_all_raw="$(docker exec ds-postgres psql -U shop -d shop -t -A -c "SELECT count(*), sum(amount) FROM orders;" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать итог PG: $pg_all_raw" >&2
  exit 1
fi
IFS='|' read -r pg_count pg_sum <<<"$pg_all_raw"
echo "PG (истина): count=$pg_count sum=$pg_sum"

# Minor-2 из ревью: раньше здесь была зашитая константа "5" — ровно то
# самое "FINAL=N при N клиентах", о тавтологичности которого предупреждает
# шапка файла. Число реальных клиентов теперь читается из PG динамически,
# поэтому не ломается на seed.sh с другим N.
if ! pg_distinct_raw="$(docker exec ds-postgres psql -U shop -d shop -t -A -c "SELECT count(distinct customer_id) FROM orders;" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать число уникальных customer_id в PG: $pg_distinct_raw" >&2
  exit 1
fi
if [[ "$ch_count" != "$pg_distinct_raw" ]]; then
  echo "ОШИБКА: в customer_totals FINAL $ch_count реальных клиентов, в PG уникальных customer_id=$pg_distinct_raw — неожиданная топология, останавливаемся" >&2
  exit 1
fi
if [[ "$ch_total_rows" != "$((ch_count + 1))" ]]; then
  echo "ОШИБКА (Important-4): в customer_totals FINAL всего строк=$ch_total_rows, ожидалось реальные($ch_count)+1 синтетический пробник — пробник не дошёл до ClickHouse либо появился не один раз" >&2
  exit 1
fi
if ! awk -v a="$ch_sum" -v b="$pg_sum" 'BEGIN{d=a-b; if(d<0)d=-d; exit !(d<0.005)}'; then
  echo "ОШИБКА: сумма в ClickHouse ($ch_sum) НЕ сходится с PG ($pg_sum) до цента после эволюции схемы — данные потерялись или исказились" >&2
  exit 1
fi
echo "ПРОВЕРЕНО (сквозной уровень, falsifiable): FINAL-сумма витрины реальных клиентов ($ch_sum) сходится с PG ($pg_sum) до цента после эволюции схемы. Ничего не потерялось и не исказилось. Плюс ровно 1 строка сверх этого — синтетический пробник (см. проверку ниже)."

if ! ch_synth_raw="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT orders, total FROM customer_totals FINAL WHERE customer_id=$SYNTH_CUSTOMER_ID" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать ClickHouse для синтетического customer_id=$SYNTH_CUSTOMER_ID: $ch_synth_raw" >&2
  exit 1
fi
if [[ -z "$ch_synth_raw" ]]; then
  echo "ОШИБКА (Important-4): синтетический пробник (customer_id=$SYNTH_CUSTOMER_ID, лишнее поле currency, см. п.5.1) НЕ дошёл до ClickHouse — falsifiable-проверка провалена" >&2
  exit 1
fi
IFS=$'\t' read -r ch_synth_orders ch_synth_total <<<"$ch_synth_raw"
if [[ "$ch_synth_orders" != "1" ]] || ! awk -v a="$ch_synth_total" 'BEGIN{d=a-9.99; if(d<0)d=-d; exit !(d<0.005)}'; then
  echo "ОШИБКА (Important-4): синтетический пробник дошёл до ClickHouse, но с искажёнными числами (orders=$ch_synth_orders total=$ch_synth_total, ожидалось orders=1 total=9.99)" >&2
  exit 1
fi
echo "ПРОВЕРЕНО (Important-4, falsifiable, реальный конвейер, не мок): синтетическое сообщение с лишним полем currency, отправленное напрямую в customer.totals в обход Streams, дошло до Go-консьюмера и ClickHouse БЕЗ ИСКАЖЕНИЙ (orders=$ch_synth_orders total=$ch_synth_total) и не помечено битым/DLQ. encoding/json Unmarshal Go по умолчанию молча игнорирует незнакомые ключи — consumer/main.go:186 использует обычный json.Unmarshal, без json.Decoder.DisallowUnknownFields()."

if ! ch_cust_raw="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT orders, total FROM customer_totals FINAL WHERE customer_id=$CUSTOMER_ID" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать ClickHouse для customer_id=$CUSTOMER_ID: $ch_cust_raw" >&2
  exit 1
fi
IFS=$'\t' read -r ch_cust_orders ch_cust_total <<<"$ch_cust_raw"
echo "CH customer_id=$CUSTOMER_ID (после консьюмера): orders=$ch_cust_orders total=$ch_cust_total"
if [[ "$ch_cust_orders" != "$pg_cust_count" ]]; then
  echo "ОШИБКА: orders в ClickHouse ($ch_cust_orders) не совпадает с PG ($pg_cust_count) для customer_id=$CUSTOMER_ID после консьюмера" >&2
  exit 1
fi
if ! awk -v a="$ch_cust_total" -v b="$pg_cust_sum" 'BEGIN{d=a-b; if(d<0)d=-d; exit !(d<0.005)}'; then
  echo "ОШИБКА: total в ClickHouse ($ch_cust_total) не совпадает с PG ($pg_cust_sum) для customer_id=$CUSTOMER_ID после консьюмера" >&2
  exit 1
fi
echo "ПРОВЕРЕНО: витрина ClickHouse для customer_id=$CUSTOMER_ID тоже сходится с PG до цента (двойная проверка: Kafka-уровень в п.5 + CH-уровень здесь)"

if ! raw_after="$(docker exec ds-clickhouse clickhouse-client -u ds --password ds -d ds -q \
  "SELECT count() FROM raw_totals" 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать ClickHouse raw_totals после консьюмера: $raw_after" >&2
  exit 1
fi
raw_delta=$((raw_after - raw_before))
if [[ "$raw_delta" != "$inserted" ]]; then
  echo "ОШИБКА: raw_totals вырос на $raw_delta записей, а консьюмер сообщил вставлено=$inserted — расхождение (потеря или незамеченный дубль)" >&2
  exit 1
fi
echo "raw_totals: +$raw_delta записей, совпадает со сводкой консьюмера (вставлено=$inserted) — без потерь и без незамеченных дублей"

if ! dlq_after_raw="$(docker exec ds-kafka /opt/kafka/bin/kafka-get-offsets.sh \
  --bootstrap-server "$BOOTSTRAP" --topic customer.totals.dlq --partitions 0 2>&1)"; then
  echo "ОШИБКА: не удалось прочитать оффсет customer.totals.dlq после прогона: $dlq_after_raw" >&2
  exit 1
fi
dlq_after="${dlq_after_raw##*:}"
if [[ "$dlq_after" != "$dlq_before" ]]; then
  echo "ОШИБКА: DLQ вырос ($dlq_before -> $dlq_after) — что-то помечено битым, хотя в этом сценарии не должно было" >&2
  exit 1
fi
echo "DLQ не изменился (offset=$dlq_after) — ни одно сообщение с новым полем не помечено битым"

# --- Итог -------------------------------------------------------------------
cat <<EOF

=== ИТОГ: эволюция схемы (currency) сквозь цепочку ===
1) Debezium (роутер: expand.json.payload=true, schemas.enable=false) — currency дошёл до orders.events дословно. ПОДТВЕРЖДЕНО прогоном (почему так — разбор в шапке файла; прогон наблюдает факт прохождения, а не механику Debezium).
2) Kafka Streams (JsonNode.get)  — не упал, независимо посчитанный агрегат совпал с PG до цента. ПОДТВЕРЖДЕНО, falsifiable.
3) customer.totals               — currency НЕ содержит (обрезан TotalsProcessor'ом, это факт топологии, не устойчивость соседнего звена).
4) Go-консьюмер (реальный трафик)     — currency физически не видит (см. п.3); честно проверено то, что можно проверить: штатный формат разобран без ошибок (битых=0), сумма в CH сходится с PG до цента.
4.1) Go-консьюмер (синтетический пробник, customer_id=$SYNTH_CUSTOMER_ID) — сообщение с лишним полем currency, отправленное напрямую в customer.totals в обход Streams, дошло до ClickHouse БЕЗ ИСКАЖЕНИЙ и не помечено битым/DLQ. ПОДТВЕРЖДЕНО, falsifiable (см. п.5.1 в шапке файла).
5) ClickHouse (FINAL)            — sum реальных клиентов $ch_sum == PG sum=$pg_sum до цента (плюс ровно 1 строка сверх — синтетический пробник). raw_totals: +$raw_delta записей без потерь/дублей. ПОДТВЕРЖДЕНО, falsifiable.
6) DLQ                           — не изменился ($dlq_after), ни одно сообщение не потеряно как "битое".

Честная оговорка (уточнена по ревью): пункт (4) на РЕАЛЬНОМ трафике —
НЕ доказательство устойчивости Go-консьюмера к незнакомым JSON-полям (currency
до него по штатному пути не доезжает никогда, см. TotalsProcessor). Это
утверждение теперь ОТДЕЛЬНО и честно доказано пунктом (4.1) — изолированным
синтетическим пробником через тот же реальный консьюмер и тот же реальный
ClickHouse, не мок. Содержательная часть демонстрации — пункты (1)-(2)
(реальная схемная эволюция, реально пройденная схемо-нейтральными звеньями),
(4.1) (устойчивость Go-консьюмера к незнакомым полям, изолированно) и (5)-(6)
(данные не потерялись и не исказились сквозь всю цепочку).
EOF
