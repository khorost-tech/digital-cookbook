#!/usr/bin/env bash
# Раздел zero-downtime, уровень A: с какого момента действует триггер двойной
# записи, созданный ПОД конкурентной нагрузкой уже открытых соединений.
#
# Проверяемое заблуждение: «уже открытые соединения пула подхватывают новый
# триггер с задержкой, поэтому часть вставок проходит мимо него». Это неверно:
# после COMMIT команды CREATE TRIGGER применимый row-level триггер срабатывает
# для каждой следующей изменяемой строки независимо от возраста соединения.
#
# КАК УСТРОЕН БАРЬЕР. Здесь он программный, а не по данным, и это принципиально.
#
# Первая версия замера делила вставки по метке clock_timestamp() — но она
# снималась ДО commit (оператор выполняется внутри транзакции). Вторая версия
# делила по max(id) — тоже не строго: значения sequence выдаются ВНЕ MVCC
# (nextval не ждёт commit и не откатывается), а SELECT max(id) видит только
# закоммиченные строки, поэтому строка могла получить id до commit DDL, а стать
# видимой после, и попасть не в ту половину. Обе версии давали убедительные
# числа при неточно определённой границе.
#
# Теперь порядок задан явно, программой:
#   фаза A — N воркеров, у КАЖДОГО своё соединение, пишут customer='before';
#   в это время выполняется CREATE TRIGGER;
#   барьер — probe ждёт строку в stdin, скрипт пишет её ТОЛЬКО после того, как
#            команда CREATE TRIGGER вернула управление (то есть после COMMIT);
#   фаза B — ТЕ ЖЕ соединения делают фиксированное число вставок customer='after'.
#
# Проверяется: среди строк 'after' нет ни одной, куда триггер не применился.
# Это утверждение уже про конкурентные соединения и про строгую границу, а не
# про приближение по идентификаторам.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

WORKERS="${WORKERS:-8}"
AFTER_EACH="${AFTER_EACH:-25}"

require_pg
echo "=== reset схемы к исходной + добавляем total_cents (expand) ==="
reset_schema
psql_c -c "ALTER TABLE orders ADD COLUMN total_cents integer" >/dev/null

BIN="$(mktemp)"
OUT="$(mktemp)"
FIFO="$(mktemp -u)"
mkfifo "$FIFO"
trap 'rm -f "$BIN" "$OUT" "$FIFO"' EXIT

( cd ../trigger-probe && GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}" \
    GOSUMDB="${GOSUMDB:-off}" go build -o "$BIN" . )

echo "=== ${WORKERS} конкурентных воркеров, у каждого своё соединение ==="
# FIFO открываем на ЧТЕНИЕ-ЗАПИСЬ (9<>): открытие только на запись блокируется,
# пока нет читателя, а probe стартует ниже — получился бы дедлок. Держать
# дескриптор открытым обязательно, иначе probe получит EOF сразу и снимет
# барьер, не дождавшись DDL.
exec 9<>"$FIFO"
"$BIN" -dsn="$DSN" -workers="$WORKERS" -after-each="$AFTER_EACH" < "$FIFO" > "$OUT" 2>&1 &
PROBE=$!

# Дать воркерам поработать со старой схемой: соединения открыты и прогреты.
sleep 2

echo "--- CREATE TRIGGER под работающей нагрузкой ---"
psql_c -q -c "
CREATE OR REPLACE FUNCTION sync_amount_total() RETURNS trigger AS \$fn\$
BEGIN
  IF NEW.total_cents IS NULL THEN NEW.total_cents := NEW.amount_cents; END IF;
  RETURN NEW;
END; \$fn\$ LANGUAGE plpgsql;
CREATE TRIGGER trg_sync BEFORE INSERT OR UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION sync_amount_total();" >/dev/null

# Команда выше вернула управление — значит COMMIT состоялся. Только теперь
# снимаем барьер: всё, что воркеры вставят дальше, заведомо идёт после commit.
echo "--- CREATE TRIGGER закоммичен; снимаем барьер ---"
echo go >&9
exec 9>&-

wait "$PROBE"
echo
cat "$OUT"

echo
echo "=== применился ли триггер, по фазам ==="
RES="$(psql_q -c "
SELECT count(*) FILTER (WHERE customer='before')
     ||'|'|| count(*) FILTER (WHERE customer='before' AND total_cents IS NULL)
     ||'|'|| count(*) FILTER (WHERE customer='after')
     ||'|'|| count(*) FILTER (WHERE customer='after'  AND total_cents IS NULL)
FROM orders WHERE customer IN ('before','after')")"
BEFORE_ALL="$(echo "$RES" | cut -d'|' -f1)"
BEFORE_NULL="$(echo "$RES" | cut -d'|' -f2)"
AFTER_ALL="$(echo "$RES" | cut -d'|' -f3)"
AFTER_NULL="$(echo "$RES" | cut -d'|' -f4)"

echo "фаза A (до барьера):    всего=${BEFORE_ALL}  из них total_cents IS NULL=${BEFORE_NULL}"
echo "фаза B (после барьера): всего=${AFTER_ALL}  из них total_cents IS NULL=${AFTER_NULL}"

echo
echo "РАЗБОР: в фазе B мимо триггера не прошла ни одна вставка — при том, что"
echo "писали их ${WORKERS} соединений, ЗАКРЕПЛЁННЫХ за воркерами (pool.Acquire)"
echo "ещё до создания триггера, и писали конкурентно."
echo
echo "Фаза A названа «до барьера», а не «до commit»: часть её строк выполнена уже"
echo "ПОСЛЕ commit DDL — барьер снимается чуть позже, — и эти строки ЗАПОЛНЕНЫ."
echo "Отсюда разница: всего ${BEFORE_ALL}, из них с NULL только ${BEFORE_NULL}. Заполненные"
echo "строки фазы A — прямое свидетельство, что триггер начал применяться сразу"
echo "после commit. Строки с NULL закрывает backfill (шаг 3 expand-contract)."
echo
echo "ПАДАЮЩИЙ ВАРИАНТ / что опровергается: если бы открытые соединения подхватывали"
echo "триггер с задержкой, ненулевое число NULL было бы и в фазе B. Наивный замер"
echo "(все NULL за окно, без барьера) показал бы ${BEFORE_NULL} «пропущенных» строк и"
echo "подтолкнул бы именно к этому ложному выводу."

# Самопроверки.
if [ "${BEFORE_ALL:-0}" -eq 0 ] || [ "${AFTER_ALL:-0}" -eq 0 ]; then
    echo "ОШИБКА: одна из фаз пуста — замер несостоятелен." >&2
    exit 1
fi
EXPECTED_AFTER=$((WORKERS * AFTER_EACH))
if [ "${AFTER_ALL}" -ne "$EXPECTED_AFTER" ]; then
    echo "ОШИБКА: в фазе B ожидалось ${EXPECTED_AFTER} вставок, получено ${AFTER_ALL}." >&2
    exit 1
fi
if [ "${AFTER_NULL:-1}" -ne 0 ]; then
    echo "ОШИБКА: после commit найдено ${AFTER_NULL} строк мимо триггера." >&2
    echo "Это противоречит документированному поведению PostgreSQL — разбирайся," >&2
    echo "а не записывай как «задержку подхвата»." >&2
    exit 1
fi
if [ "${BEFORE_NULL:-0}" -eq 0 ]; then
    echo "ОШИБКА: в фазе A нет ни одной строки с NULL — контраст не показан." >&2
    exit 1
fi
echo
echo "OK: фаза B — ${AFTER_ALL} вставок ${WORKERS} конкурентными соединениями, 0 мимо триггера;"
echo "фаза A — ${BEFORE_NULL} строк с NULL из ${BEFORE_ALL}."
