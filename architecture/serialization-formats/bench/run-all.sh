#!/usr/bin/env bash
# Сводный прогон стенда (Задача 9): гоняет все четыре оси ОДНИМ RUN_ID,
# метит им все четыре фикстуры и ставит `fixtures/COMPLETE` ТОЛЬКО после
# того, как прошли ВСЕ оси И весь набор тестов -- питоновский, Go и Java.
#
# ПОЧЕМУ ВЕСЬ НАБОР ТЕСТОВ, А НЕ ТОЛЬКО СВОЙ. Полный набор тестов уже был
# красным целую задачу подряд, и никто этого не заметил, потому что каждая
# задача гоняла только свою часть (`python -m unittest scripts.test_size_*`
# вместо `discover`). Проверка существовала, была права и молчала --
# сводный прогон обязан гонять ВСЁ: `python -m unittest discover scripts`,
# `go test ./...`, `mvn test` (в том же контейнере, что и сборка).
#
# ПОЧЕМУ RUN_ID, А НЕ ПОЛАГАТЬСЯ НА МЕТКУ ВРЕМЕНИ В ШАПКЕ. Каждый
# run-*.sh уже пишет метку времени в комментарий шапки своей фикстуры, но
# метки четырёх осей снимаются в разное время ДАЖЕ внутри одного сводного
# прогона -- по ним нельзя доказать, что все четыре фикстуры принадлежат
# ОДНОМУ запуску run-all.sh, а не подобраны руками из разных прогонов.
# RUN_ID -- один и тот же литерал, вставляемый ОТДЕЛЬНОЙ строкой
# комментария (парсер фикстур игнорирует строки, начинающиеся с "#" --
# см. `data_lines` в scripts/analyze-*.py) в КАЖДУЮ из четырёх фикстур
# ЭТИМ сценарием, СРАЗУ ПОСЛЕ того как соответствующий run-*.sh её
# перезаписал. Если кто-то потом руками пересоберёт одну ось в обход
# run-all.sh, её RUN_ID перестанет совпадать с остальными тремя --
# расхождение видно простым grep по фикстурам, а не остаётся обещанием
# "мы точно не перепутали".
#
# ПРО РЕЕСТР (ось 3, run-need-schema.sh). Сценарий поднимает контейнер
# Apicurio и сам гасит его в СВОЁМ trap на EXIT -- это гарантия ДО этого
# круга правок (см. bench/run-need-schema.sh). Сводный прогон здесь ничего
# не переизобретает: run-need-schema.sh вызывается как обычный дочерний
# процесс, и его EXIT-trap отрабатывает независимо от того, кто его вызвал
# -- run-all.sh напрямую или человек руками. Дополнительный trap ниже,
# гасящий тот же контейнер по ИМЕНИ, -- вторая линия защиты НА СЛУЧАЙ,
# если сам run-all.sh упадёт способом, который помешает его дочернему
# процессу доработать (например, получит сигнал раньше, чем успеет
# запуститься run-need-schema.sh, но контейнер от ПРЕДЫДУЩЕГО оборванного
# прогона всё ещё жив) -- не замена внутреннему trap, а перестраховка.
set -euo pipefail

STAND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BENCH="$STAND/bench"
FIXTURE_DIR="$STAND/fixtures"
COMPLETE_MARKER="$FIXTURE_DIR/COMPLETE"
JAVA_IMAGE="maven:3.9-eclipse-temurin-25"
# То же имя контейнера, что в bench/run-need-schema.sh -- перестраховка
# должна гасить именно его, а не какой-то свой.
REGISTRY_CONTAINER="serialization-formats-need-registry"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}${RANDOM}"

cleanup_registry() {
  # Вторая линия защиты (см. преамбулу) -- тихая, без вывода "не найдено".
  docker rm -f "$REGISTRY_CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup_registry EXIT

echo "== сводный прогон serialization-formats, RUN_ID=$RUN_ID ==" >&2

mkdir -p "$FIXTURE_DIR"

# Маркер предыдущего УСПЕШНОГО прогона не должен пережить начало нового:
# иначе упавший на середине текущий прогон оставил бы старый
# fixtures/COMPLETE, и его падение осталось бы незамеченным тем, кто
# проверяет только наличие файла, а не его RUN_ID.
rm -f "$COMPLETE_MARKER"

stamp() {
  # Вставляет строку "# RUN_ID: ..." ПЕРЕД финальным маркером COMPLETE
  # внутри уже записанной фикстуры -- парсер игнорирует строки,
  # начинающиеся с "#" (data_lines в scripts/analyze-*.py), поэтому
  # добавление комментария не трогает ни одну проверку числа строк или
  # содержимого. Фикстура обязана существовать и заканчиваться маркером
  # COMPLETE -- иначе соответствующая ось упала, и сводный прогон обязан
  # остановиться здесь, а не молча продолжить со старой фикстурой.
  local fixture="$1"
  local axis="$2"
  if [ ! -f "$fixture" ]; then
    echo "ось $axis не создала фикстуру: $fixture" >&2
    exit 1
  fi
  local last
  last="$(tail -n1 "$fixture")"
  if [ "$last" != "COMPLETE" ]; then
    echo "ось $axis: фикстура $fixture не завершена маркером COMPLETE -- прогон упал" >&2
    exit 1
  fi
  local tmp="$fixture.run-all.tmp"
  sed '$d' "$fixture" > "$tmp"
  printf '# RUN_ID: %s\nCOMPLETE\n' "$RUN_ID" >> "$tmp"
  mv "$tmp" "$fixture"
  echo "  $axis: $fixture помечена RUN_ID=$RUN_ID" >&2
}

echo "== ось 1/4: размер (bench/run-size.sh) ==" >&2
"$BENCH/run-size.sh"
stamp "$FIXTURE_DIR/size.txt" "размер"

echo "== ось 2/4: эволюция схемы (bench/run-evolution.sh) ==" >&2
"$BENCH/run-evolution.sh"
stamp "$FIXTURE_DIR/evolution.txt" "эволюция"

echo "== ось 3/4: доступность схемы (bench/run-need-schema.sh) ==" >&2
# Поднимает и гасит реестр Apicurio САМ, в собственном trap на EXIT --
# см. преамбулу про то, зачем здесь всё равно есть вторая линия защиты.
"$BENCH/run-need-schema.sh"
stamp "$FIXTURE_DIR/need.txt" "доступность"

echo "== ось 4/4: перекрёстное чтение (bench/run-cross.sh) ==" >&2
"$BENCH/run-cross.sh"
stamp "$FIXTURE_DIR/cross.txt" "перекрёстное чтение"

echo "== все четыре оси сняты, RUN_ID=$RUN_ID -- запуск ПОЛНОГО набора тестов ==" >&2

echo "-- Python: python -m unittest discover scripts (ВЕСЬ набор, не только свежие тесты) --" >&2
( cd "$STAND" && python -m unittest discover scripts -v )

echo "-- Go: go test ./... --" >&2
( cd "$STAND/go" && go test ./... )

echo "-- Java: mvn test (контейнер, тот же образ, что и сборка) --" >&2
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$STAND":/stand -w /stand/java "$JAVA_IMAGE" \
  mvn -q -B test

echo "== весь набор тестов прошёл зелёным -- ставим fixtures/COMPLETE ==" >&2

{
  echo "# Сводный маркер завершения (serialization-formats, Задача 9)."
  echo "# RUN_ID=$RUN_ID"
  echo "# Прогон завершён: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# Появление этого файла означает: все четыре оси (fixtures/size.txt,"
  echo "# evolution.txt, need.txt, cross.txt) сняты ЭТИМ RUN_ID -- сверить"
  echo "# командой:"
  echo "#   grep -h '# RUN_ID:' fixtures/size.txt fixtures/evolution.txt \\"
  echo "#     fixtures/need.txt fixtures/cross.txt"
  echo "# -- и ВЕСЬ набор тестов (python -m unittest discover scripts,"
  echo "# go test ./..., mvn test) прошёл зелёным ДО того, как этот файл"
  echo "# появился. Отсутствие файла или несовпадающие RUN_ID в фикстурах"
  echo "# означают, что прогон либо не завершался, либо смешан из разных"
  echo "# запусков (ось пересобрана вручную в обход run-all.sh)."
  echo "COMPLETE"
} > "$COMPLETE_MARKER"

echo "== сводный прогон завершён. RUN_ID=$RUN_ID ==" >&2
echo "Маркер: $COMPLETE_MARKER" >&2
