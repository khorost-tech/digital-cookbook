#!/usr/bin/env bash
# failure.sh — что происходит с кластером и с ответами на запросы, когда узлы
# выходят из строя. Сохраняет всё в out/failure.txt.
#
# Проверяются три состояния:
#   1) исходное — все четыре инстанса Online, запрос возвращает полный набор;
#   2) потерян ОДИН узел репликасета — реплика подхватывает, ответ полный;
#   3) потерян ВЕСЬ репликасет — и вот здесь главное: запрос НЕ падает, а
#      молча возвращает неполный результат.
#
# Третий случай — причина, по которой этот сценарий вообще написан. Отказ,
# который выглядит как успех, опаснее отказа, который выглядит как отказ.
#
# ВАЖНО: узлы для остановки определяются ПО ФАКТУ — какие контейнеры несут
# инстансы одного репликасета. Жёстко зашитые имена здесь недопустимы:
# привязка контейнеров к репликасетам меняется от пересоздания к пересозданию,
# и на другой раскладке скрипт остановил бы узлы из РАЗНЫХ репликасетов. Тогда
# оба репликасета остались бы живыми, ответ был бы полным, скрипт напечатал бы
# «репликасет не потерян» и завершился успешно — а на этом сценарии построен
# главный вывод статьи. Молчаливо неверный результат здесь недопустим вдвойне.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
STAND="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=ops/cluster-lib.sh
source "$STAND/ops/cluster-lib.sh"
mkdir -p "$DIR/out"
export MSYS_NO_PATHCONV=1

# Вывод дублируется в файл через перенаправление, а НЕ через `{ ... } | tee`:
# в пайплайне тело блока исполняется в субшелле, и `exit 1` из него не
# завершает скрипт — он продолжает работу и печатает финальное «ok» после
# провала. Ровно это здесь и происходило.
exec > >(tee "$DIR/out/failure.txt") 2>&1

EXPECTED=200000
fail=0

q() { docker exec picodata-origin psql "$1" -tAc "$2" 2>&1 | tr -d '\r'; }
states() { docker exec picodata-origin psql "$1" -tAc \
    "SELECT name, replicaset_name, current_state FROM _pico_instance" 2>&1 | tr -d '\r'; }

# Имя инстанса и его репликасет: спрашиваем сам инстанс, а не выводим из
# номера контейнера. pico.instance_info() отдаёт оба поля сразу.
info_of() {
    docker exec -i "$1" picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null
\lua
pico.instance_info()
EOF
}
# Сколько из указанных контейнеров сообщают о себе как о Raft-лидере.
# Здоровье управляющей плоскости — это ровно один лидер, а не «SQL отвечает».
raft_leaders() {
    local n=0 c role
    for c in "$@"; do
        docker ps --format '{{.Names}}' | grep -qx "$c" || continue
        role="$(docker exec -i "$c" picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null | grep -E '^\s*raft_state:' | awk '{print $2}'
\lua
pico.raft_status()
EOF
)"
        [ "$role" = "Leader" ] && n=$(( n + 1 ))
    done
    echo "$n"
}
instance_of()  { info_of "$1" | grep -E '^\s+name:'            | awk '{print $2}' | tr -d '[:space:]'; }
replicaset_of() { info_of "$1" | grep -E '^\s+replicaset_name:' | awk '{print $2}' | tr -d '[:space:]'; }

DSN_VIA() { echo "postgres://admin:Picodata1@$1:5432/picodata?sslmode=disable"; }

    # Предусловие — полный барьер готовности, а не «Online + лидер». Проверяются
    # ещё состояние репликасетов и РАСПРЕДЕЛЕНИЕ бакетов: наблюдался кластер,
    # где при Online и лидере все 3000 бакетов лежали на одном репликасете, и
    # сценарий принимал такое состояние за исходное. Выводы после этого
    # недействительны, а выглядят убедительно.
    echo "=== предусловие: полный барьер готовности кластера ==="
    if ! cl_wait_ready 4 2 120 pd-1 pd-2 pd-3 pd-4; then
        echo "    пересоздайте кластер перед прогоном: bash ops/up.sh 0.2.0" >&2
        exit 1
    fi
    cl_dump_state pd-1 pd-2 pd-3 pd-4
    # И согласованность данных: реплики одного репликасета должны совпадать, а
    # сумма по уникальным репликасетам — давать полный набор.
    if ! cl_check_data "$EXPECTED" pd-1 pd-2 pd-3 pd-4; then
        echo "!!! данные не в согласованном состоянии — прогон недействителен" >&2
        exit 1
    fi

    echo
    echo "=== определяем, какие контейнеры несут один репликасет ==="
    # Карта «контейнер -> репликасет» строится по факту, а не по номерам.
    declare -A RS_OF=()
    for c in pd-1 pd-2 pd-3 pd-4; do
        name="$(instance_of "$c")"
        rs="$(replicaset_of "$c")"
        if [ -z "$name" ] || [ -z "$rs" ]; then
            echo "!!! не удалось опросить ${c}: инстанс '${name}', репликасет '${rs}'" >&2
            exit 1
        fi
        RS_OF[$c]="$rs"
        printf "  %-6s инстанс %-14s репликасет %s\n" "$c" "$name" "$rs"
    done

    # Выбираем репликасет-жертву и два его контейнера; выживший узел — из другого.
    victim_rs=""; victim=(); survivor=""
    for c in pd-1 pd-2 pd-3 pd-4; do
        rs="${RS_OF[$c]}"
        [ -n "$rs" ] || continue
        if [ -z "$victim_rs" ]; then victim_rs="$rs"; fi
        if [ "$rs" = "$victim_rs" ]; then victim+=("$c"); else survivor="${survivor:-$c}"; fi
    done

    if [ "${#victim[@]}" -ne 2 ] || [ -z "$survivor" ]; then
        echo "!!! не удалось разложить контейнеры по репликасетам: жертв ${#victim[@]}, выживший '${survivor}'" >&2
        exit 1
    fi
    echo "  жертвы (репликасет ${victim_rs}): ${victim[0]} ${victim[1]}"
    echo "  опрашивать будем через: ${survivor}"
    SURV_DSN="$(DSN_VIA "picodata-${survivor#pd-}")"

    echo
    echo "=== 0. распределение данных по узлам ==="
    for c in pd-1 pd-2 pd-3 pd-4; do
        n="$(docker exec -i "$c" picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null | grep -E "^- [0-9]+" | tr -d '[:space:]-'
\lua
box.space.products:len()
EOF
)"
        b="$(docker exec -i "$c" picodata admin /var/lib/picodata/admin.sock <<'EOF' 2>/dev/null | grep "active:" | tr -d '[:space:]' | sed 's/active://'
\lua
require('vshard').storage.info().bucket
EOF
)"
        printf "  %-6s строк: %-8s бакетов: %s\n" "$c" "$n" "$b"
    done
    echo "  Числа выше — ИЗМЕРЕННЫЕ в этом прогоне, а не эталон: соотношение"
    echo "  строк между репликасетами непостоянно даже после барьера готовности."
    echo "  Наблюдавшиеся примеры при неизменных 1500 бакетах на репликасет:"
    echo "  99776/100224, 123103/76897, 124807/75193, 133249/66751. Это перечень"
    echo "  исходов, а НЕ распределение частот — систематической серии с подсчётом"
    echo "  вероятностей не проводилось. Строки по ДИАПАЗОНАМ bucket_id при этом"
    echo "  делятся детерминированно (100224/99776 — чистый хеш ключа), значит"
    echo "  дело в том, КАКИЕ бакеты достались репликасету. Механизм назначения"
    echo "  стендом не установлен."

    echo
    echo "=== 1. исходное состояние ==="
    got="$(q "$(DSN_VIA picodata-1)" 'SELECT count(*) FROM products')"
    echo "  count(*) = ${got}  (ожидается ${EXPECTED})"
    [ "$got" = "$EXPECTED" ] || { echo "  !!! исходный набор уже неполный" >&2; fail=1; }

    echo
    echo "=== 2. останавливаем ОДИН узел репликасета ${victim_rs} (${victim[0]}) ==="
    docker stop "${victim[0]}" >/dev/null 2>&1
    sleep 12
    states "$SURV_DSN" | sed 's/^/  /'
    got="$(q "$SURV_DSN" 'SELECT count(*) FROM products')"
    echo "  count(*) = ${got}"
    if [ "$got" = "$EXPECTED" ]; then
        echo "  ok: реплика подхватила, ответ полный"
    else
        echo "  !!! ответ неполный уже при потере одного узла" >&2; fail=1
    fi

    echo
    echo "=== 3. останавливаем ВТОРОЙ узел того же репликасета (${victim[1]}) ==="
    docker stop "${victim[1]}" >/dev/null 2>&1
    sleep 15
    states "$SURV_DSN" | sed 's/^/  /'
    got="$(q "$SURV_DSN" 'SELECT count(*) FROM products')"
    echo "  count(*) = ${got}  (в целом кластере ${EXPECTED})"
    case "$got" in
        "$EXPECTED")
            echo "  !!! НЕОЖИДАННО: ответ полный, хотя репликасет потерян." >&2
            echo "      Либо остановлены узлы разных репликасетов, либо поведение" >&2
            echo "      изменилось — вывод статьи требует перепроверки." >&2
            fail=1 ;;
        ''|*[!0-9]*)
            echo "  запрос завершился ошибкой (а не молча неполным результатом):"
            echo "    ${got}" | head -3
            echo "  Это ИНОЕ поведение, чем описано в статье, — перепроверьте текст." >&2
            fail=1 ;;
        *)
            echo "  ГЛАВНОЕ: запрос НЕ упал, а вернул неполный результат."
            echo "  Ни ошибки, ни предупреждения о недоступной части данных."
            echo "  агрегация по категориям на неполном кластере:"
            q "$SURV_DSN" 'SELECT category, count(*) FROM products GROUP BY category' | sed 's/^/    /' ;;
    esac

    echo
    echo "=== 4. восстановление ==="
    docker start "${victim[0]}" "${victim[1]}" >/dev/null 2>&1
    sleep 30

    # Восстановление — это ДВА разных события, и их нельзя смешивать. Именно об
    # этом сама статья: здоровье кластера нельзя выводить из успешности SQL.
    #  (а) данные снова доступны — проверяется count(*);
    #  (б) управляющая плоскость восстановилась — проверяется наличие РОВНО
    #      одного Raft-лидера. SQL может отдавать полный результат, пока Raft
    #      ещё переизбирает лидера, — и это отдельный, самостоятельный результат.
    got="$(q "$(DSN_VIA picodata-1)" 'SELECT count(*) FROM products')"
    echo "  данные: count(*) = ${got}"
    if [ "$got" = "$EXPECTED" ]; then
        echo "    ok: данные снова доступны (были недоступны, не потеряны)"
    else
        echo "    !!! после восстановления набор всё ещё неполный" >&2; fail=1
    fi

    echo "  управляющая плоскость: ждём ровно одного Raft-лидера (до 90 с)"
    leader_deadline=$(( $(date +%s) + 90 ))
    leaders=0
    while [ "$(date +%s)" -lt "$leader_deadline" ]; do
        leaders="$(raft_leaders pd-1 pd-2 pd-3 pd-4)"
        [ "$leaders" -eq 1 ] && break
        sleep 5
    done
    echo "    Raft-лидеров: ${leaders}"
    if [ "$leaders" -eq 1 ]; then
        echo "    ok: управляющая плоскость восстановилась"
    else
        echo "    !!! лидер НЕ восстановился за 90 с (лидеров: ${leaders})." >&2
        echo "    Это самостоятельный результат: SQL мог отдавать полный ответ," >&2
        echo "    пока Raft оставался без лидера. Здоровье кластера и доступность" >&2
        echo "    данных — разные вещи, и здесь они разошлись." >&2
        fail=1
    fi
echo
# Уборка: сценарий останавливал и поднимал узлы, и после этого Raft может
# остаться в переходном состоянии (наблюдался узел в роли Candidate без
# стабильного лидера ещё долго после того, как SQL уже отдавал полный ответ —
# ровно то расхождение «данные доступны / плоскость не восстановилась», о
# котором сам сценарий и говорит). Чтобы следующие сценарии не наследовали
# нестабильную топологию, пересоздаём кластер с нуля. KEEP_CLUSTER=1 отключает.
if [ "${KEEP_CLUSTER:-0}" != "1" ]; then
    echo "=== уборка: пересоздаём кластер (Raft мог остаться нестабильным) ==="
    STAND_WIN="$(cd "$(dirname "$0")/.." && (pwd -W 2>/dev/null || pwd))"
    docker compose -f "$STAND_WIN/compose/cluster.yml" down -v >/dev/null 2>&1
    SKIP_DATASET=1 bash "$STAND/ops/up.sh" 0.2.0 >/dev/null 2>&1
    # Уборка проверяется тем же барьером, что и предусловие: Online и лидера
    # мало, нужно ещё распределение бакетов и согласованность данных — иначе
    # следующий сценарий начнёт на неготовом кластере.
    if cl_wait_ready 4 2 120 pd-1 pd-2 pd-3 pd-4 && cl_check_data "$EXPECTED" pd-1 pd-2 pd-3 pd-4; then
        echo "  ok: кластер восстановлен и готов"
        cl_dump_state pd-1 pd-2 pd-3 pd-4
    else
        echo "!!! после пересоздания кластер не в рабочем состоянии" >&2
        fail=1
    fi
fi

if [ "$fail" -ne 0 ]; then
    echo "ПРОВАЛ: сценарий отработал не так, как описано в статье — см. вывод выше" >&2
    exit 1
fi
echo "ok: поведение при отказах совпадает с описанным в статье"
