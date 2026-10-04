#!/usr/bin/env bash
# Воспроизведение оценок времени из README и статьи.
#
# Меряет два набора в каждом языке — юнит и интеграционный — и печатает медиану
# N прогонов. Медиану, а не среднее: один выброс (JIT, скачок образа, соседний
# процесс) сдвигает среднее и не сдвигает медиану.
#
# ВАЖНО про сами числа. Они меряют ОКРУЖЕНИЕ не меньше, чем язык: тот же
# Python-набор дал 0.33 с на нативной ФС и 4.13 с при запуске с примонтированного
# диска. Поэтому в статье точной межъязыковой таблицы нет — только порядок
# величин. Снимайте свои.
#
# Методика:
#   * берётся время, которое печатает САМ тест-раннер, а не wall-clock: у
#     wall-clock сверху лежит старт go/JVM/pytest, и он у языков разный;
#   * у Go время суммируется по пакетам — единого агрегата он не печатает;
#   * у Java суммируется «Time elapsed» по тест-классам;
#   * перед серией — холостой прогон (прогрев), его результат выбрасывается;
#     образ postgres:18.1-alpine должен быть уже скачан;
#   * go test идёт с -count=1 — иначе Go отдаст закэшированный результат
#     («ok ... (cached)»), и вы намеряете скорость чтения кэша, а не тестов;
#   * демо-тесты (падающие намеренно) исключены — они не часть набора.
#
#   ./timing-matrix.sh              # 5 прогонов, все языки
#   ./timing-matrix.sh 3
#   ./timing-matrix.sh 5 go         # только Go — если тулчейны в разных окружениях
#
# Требуется: go, mvn + JDK 21, python/.venv, Docker (для интеграционных).
set -uo pipefail

N="${1:-5}"
ONLY="${2:-all}"    # all | go | java | python — для смешанных окружений, где
                    # тулчейны живут в разных местах (Go на хосте, Maven в WSL).
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

die()  { echo "ОШИБКА: $1" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "не найден $1"; }
need docker
case "$ONLY" in
  all)    need go; need mvn; [ -x "$HERE/python/.venv/bin/pytest" ] || die "нет python/.venv" ;;
  go)     need go ;;
  java)   need mvn ;;
  python) [ -x "$HERE/python/.venv/bin/pytest" ] || die "нет python/.venv" ;;
  *) die "второй аргумент: all | go | java | python" ;;
esac

median() { sort -n | awk '{a[NR]=$1} END{if(NR==0){print "н/д"; exit} print (NR%2)?a[(NR+1)/2]:(a[NR/2]+a[NR/2+1])/2}'; }

# Прогон ловит вывод и КОД ВОЗВРАТА раздельно. Без проверки rc упавший или
# несобравшийся прогон печатает крошечное число (из строки «FAIL ... 0.01s») и
# выглядит рекордно быстрым набором вместо ошибки. Проверено подставным `go`,
# который печатал правдоподобные секунды и падал с кодом 1: прошлая версия
# скрипта посчитала его медиану и завершилась успехом.
#
# RUN_OUT/RUN_RC — глобальные: прятать прогон в $(...) нельзя, там exit убивает
# только subshell (ровно на этом горел и property-matrix.sh).
RUN_OUT=''
RUN_RC=0
run_cmd() { RUN_OUT="$("$@" 2>&1)"; RUN_RC=$?; }

# Извлекатели времени из вывода раннера.
sum_go()   { grep -oE '[0-9]+\.[0-9]+s' <<<"$RUN_OUT" | tr -d 's' | awk '{s+=$1} END{if(NR)printf "%.2f", s}'; }
first_go() { grep -oE '[0-9]+\.[0-9]+s' <<<"$RUN_OUT" | tr -d 's' | head -1; }
sum_java() { grep -oE 'Time elapsed: [0-9.]+ s -- in' <<<"$RUN_OUT" | grep -oE '[0-9.]+' | awk '{s+=$1} END{if(NR)printf "%.2f", s}'; }
java_it()  { grep -oE 'Time elapsed: [0-9.]+ s -- in tech.khorost.across.PostgresStoreTest' <<<"$RUN_OUT" \
             | grep -oE '[0-9.]+' | head -1; }
py_time()  { grep -oE 'in [0-9.]+s' <<<"$RUN_OUT" | grep -oE '[0-9.]+'; }

# measure печатает строку САМ и валит весь скрипт при беде — по той же причине
# его нельзя звать через $(...).
measure() { # $1=подпись $2=извлекатель $3...=команда
  local label="$1" extract="$2"; shift 2
  local vals=() v i
  run_cmd "$@"                                   # прогрев, результат выбрасываем
  for i in $(seq 1 "$N"); do
    run_cmd "$@"
    if [ "$RUN_RC" -ne 0 ]; then
      echo >&2; echo "--- хвост вывода ---" >&2; printf '%s\n' "$RUN_OUT" | tail -12 >&2
      die "прогон «$label» (№$i) упал с кодом $RUN_RC — время недействительно"
    fi
    v="$("$extract")"
    [ -n "$v" ] || { echo "--- хвост вывода ---" >&2; printf '%s\n' "$RUN_OUT" | tail -12 >&2
                     die "прогон «$label» (№$i) не дал времени — раннер ничего не напечатал"; }
    vals+=("$v")
  done
  printf '  %-28s медиана %6s с   (прогоны: %s)\n' "$label" \
    "$(printf '%s\n' "${vals[@]}" | median)" "${vals[*]}"
}

go_unit_cmd()    { (cd "$HERE/go" && go test ./pricing/ ./service/ -count=1); }
go_integ_cmd()   { (cd "$HERE/go" && go test -tags=integration ./store/ -count=1); }
java_unit_cmd()  { (cd "$HERE/java" && mvn -B test); }
java_integ_cmd() { (cd "$HERE/java" && mvn -B -Dgroups=integration -Dexcluded.groups= test); }
py_unit_cmd()    { (cd "$HERE/python" && ./.venv/bin/pytest -q); }
py_integ_cmd()   { (cd "$HERE/python" && ./.venv/bin/pytest -m integration -q); }

want() { [ "$ONLY" = all ] || [ "$ONLY" = "$1" ]; }

echo "Медиана $N прогонов, время по отчёту тест-раннера, кэши прогреты."
echo "Числа зависят от окружения (ФС, машина) сильнее, чем от языка."
echo
echo "=== юнит-наборы (без интеграционных и демо) ==="
want go     && measure "Go: pricing + service"   sum_go   go_unit_cmd
want java   && measure "Java: mvn test"          sum_java java_unit_cmd
want python && measure "Python: pytest"          py_time  py_unit_cmd
echo
echo "=== интеграционные (2 теста, один контейнер на пакет) ==="
want go     && measure "Go: store"               first_go go_integ_cmd
want java   && measure "Java: PostgresStoreTest" java_it  java_integ_cmd
want python && measure "Python: -m integration"  py_time  py_integ_cmd
exit 0
