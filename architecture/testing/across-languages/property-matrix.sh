#!/usr/bin/env bash
# Воспроизведение таблицы «находит ли наивное свойство нарушение» из README.
#
# Гоняет серию прогонов каждой конфигурации и считает, в скольких движок нашёл
# нарушение монотонности цены.
#
# Перед КАЖДЫМ прогоном чистится кэш найденных примеров — иначе движок
# переигрывает прошлую находку первой, и замер показывает не «нашёл заново», а
# «помнил с прошлого раза»:
#   rapid      → go/pricing/testdata/     jqwik → java/.jqwik-database
#   Hypothesis → python/.hypothesis/ (+ --hypothesis-seed=random)
#
# Результат вероятностный. «0 из 20» означает «редко», а не «никогда»: при нуле
# находок из 20 прогонов двусторонний 95% интервал (Клоппер–Пирсон) для
# вероятности находки за прогон — [0%, 16.9%].
#
#   ./property-matrix.sh              # как в статье: 20 прогонов на дефолт, 5 на остальное
#   ./property-matrix.sh 3 2 2        # быстрее: дефолт / большой бюджет / доменный
#   EQUAL_BUDGET=1 ./property-matrix.sh   # + контроль на равном бюджете (1000 у всех)
#
# Требуется: go, mvn + JDK 21, python-venv в python/.venv. Docker не нужен.
set -uo pipefail

DEFAULT_RUNS="${1:-20}"   # наивный генератор, бюджет по умолчанию
# У jqwik ячейка «дефолт» в README снята за 40 прогонов (два захода по 20: они
# дали 4 и 1 находку — разброс сам по себе показателен). Чтобы воспроизвести
# именно её: JQWIK_RUNS=40 ./property-matrix.sh
JQWIK_RUNS="${JQWIK_RUNS:-$DEFAULT_RUNS}"
LARGE_RUNS="${2:-5}"      # наивный генератор, 20 000 примеров
DOMAIN_RUNS="${3:-5}"     # доменный генератор, бюджет по умолчанию

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Проверки окружения: пропавший или неподходящий инструмент не должен
# --- превращаться в тихое «0 из N».
die() { echo "ОШИБКА: $1" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "не найден $1 — замер невозможен"; }
need go
need mvn
need java
JAVA_MAJOR="$(java -version 2>&1 | head -1 | grep -oE '"[0-9]+' | tr -d '"')"
[ -n "$JAVA_MAJOR" ] && [ "$JAVA_MAJOR" -ge 21 ] || \
  die "нужен JDK 21+, найден «$(java -version 2>&1 | head -1)» — стенд собирается под release 21"
[ -x "$HERE/python/.venv/bin/pytest" ] || die \
  "нет python/.venv — создайте: cd python && python3 -m venv .venv && ./.venv/bin/pip install -r requirements.txt"

FOUND_MSG='заплатить больше'
LAST_OUT=''

# Таблица истинности, из которой следует вся механика:
#   rc = 0,  сообщения нет  → нарушение НЕ найдено (тест прошёл);
#   rc != 0, сообщение есть → нарушение НАЙДЕНО (тест упал именно на нём);
#   rc != 0, сообщения нет  → прогон НЕ СОСТОЯЛСЯ (не тот JDK, не собралось,
#                             JVM не стартовала) — это ошибка эксперимента,
#                             а не результат;
#   rc = 0,  сообщение есть → небывальщина, тоже останавливаемся.
#
# Почему не проще. Нельзя писать
#     if some_test | grep -q "$FOUND_MSG"; then
# по двум причинам сразу:
#   1) при set -o pipefail код пайплайна берётся от УПАВШЕГО теста — а падает он
#      именно тогда, когда нарушение НАЙДЕНО. Находка засчитывается промахом.
#      Ровно на этом скрипт врал в первой версии: печатал нули там, где
#      нарушение находилось;
#   2) неудачный запуск неотличим от «не нашёл» — grep одинаково молчит.
# Поэтому вывод и код возврата ловим раздельно и решаем по обоим.
#
# Возвращает: 0 — нашёл, 1 — не нашёл, 2 — прогон не состоялся.
run_once() {
  local out rc
  out="$("$@" 2>&1)"; rc=$?
  LAST_OUT="$out"
  if grep -qF "$FOUND_MSG" <<<"$out"; then
    [ "$rc" -eq 0 ] && return 2   # сообщение есть, а тест зелёный — так не бывает
    return 0
  fi
  [ "$rc" -eq 0 ] && return 1
  return 2
}

# Счётчик серии. Печатает строку САМ — вызывать через $(...) нельзя: в
# подстановке команд exit убьёт только subshell, внешний echo продолжит работу,
# и невалидный эксперимент завершится кодом 0. На этом скрипт горел.
series() { # $1=подпись $2=N $3...=команда
  local label="$1" n="$2"; shift 2
  local found=0 i
  for i in $(seq 1 "$n"); do
    run_once "$@"
    case $? in
      0) found=$((found + 1)) ;;
      1) ;;
      2) echo >&2
         echo "ОШИБКА: прогон «$label» (№$i) не состоялся — эксперимент недействителен." >&2
         echo "--- хвост вывода ---" >&2
         printf '%s\n' "$LAST_OUT" | tail -15 >&2
         exit 1 ;;
    esac
  done
  printf '  %-34s %s из %s\n' "$label" "$found" "$n"
}

go_run() { rm -rf "$HERE/go/pricing/testdata"
           (cd "$HERE/go" && go test -tags=property_demo -run "$1" ./pricing/ -rapid.checks="$2" -count=1); }
# ВАЖНО: mvn test, а не surefire:test. Прямой вызов плагина пропускает фазу
# lifecycle, на которой maven-dependency-plugin подставляет путь к jar'у Mockito
# в -javaagent, и JVM падает («Error opening zip file or JAR manifest missing»).
# Тестов при этом ноль — и снаружи это неотличимо от «нарушение не найдено».
java_run() { rm -rf "$HERE/java/.jqwik-database"
             (cd "$HERE/java" && mvn -B -Dgroups=property-demo -Dexcluded.groups= \
                -Dtest="PricingProperties#$1" -DfailIfNoSpecifiedTests=false test); }
py_run() { rm -rf "$HERE/python/.hypothesis"
           (cd "$HERE/python" && PROP_EXAMPLES="$2" ./.venv/bin/pytest -m property_demo \
              -k "$1" -q --hypothesis-seed=random); }

echo "Прогонов: дефолт=$DEFAULT_RUNS, 20000=$LARGE_RUNS, доменный=$DOMAIN_RUNS"
echo "Кэш найденных примеров чистится перед каждым прогоном."
echo
echo "=== наивный генератор, бюджет ПО УМОЛЧАНИЮ (у движков он разный!) ==="
series "rapid, 100 примеров"      "$DEFAULT_RUNS" go_run   Наивн 100
series "jqwik, 1000 попыток"      "$JQWIK_RUNS"   java_run монотонностьНаивныйДефолтныйБюджет
series "Hypothesis, 100 примеров" "$DEFAULT_RUNS" py_run   наивный 100

if [ -n "${EQUAL_BUDGET:-}" ]; then
  echo
  echo "=== контроль на РАВНОМ бюджете: все по 1000 ==="
  echo "  (отвечает на вопрос, объясняется ли разница дефолтов одним лишь бюджетом)"
  series "rapid @100"       "$DEFAULT_RUNS" go_run   Наивн 100
  series "jqwik @100"       "$DEFAULT_RUNS" java_run монотонностьНаивныйМалыйБюджет
  series "Hypothesis @100"  "$DEFAULT_RUNS" py_run   наивный 100
  echo
  series "rapid @1000"      "$DEFAULT_RUNS" go_run   Наивн 1000
  series "jqwik @1000"      "$JQWIK_RUNS"   java_run монотонностьНаивныйДефолтныйБюджет
  series "Hypothesis @1000" "$DEFAULT_RUNS" py_run   наивный 1000
fi

echo
echo "=== наивный генератор, 20 000 примеров ==="
series "rapid"      "$LARGE_RUNS" go_run   Наивн 20000
series "jqwik"      "$LARGE_RUNS" java_run монотонностьНаивныйБольшойБюджет
series "Hypothesis" "$LARGE_RUNS" py_run   наивный 20000

echo
echo "=== доменный генератор, бюджет по умолчанию ==="
series "rapid"      "$DOMAIN_RUNS" go_run   Доменн 100
series "jqwik"      "$DOMAIN_RUNS" java_run монотонностьДоменныйГенератор
series "Hypothesis" "$DOMAIN_RUNS" py_run   доменный 100
