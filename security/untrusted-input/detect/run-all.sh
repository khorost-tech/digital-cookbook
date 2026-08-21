#!/usr/bin/env bash
# Полный цикл: сборка -> раскладка -> матрица -> замеры. Один запуск = один снимок.
#
# Зачем отдельный скрипт, если есть четыре по отдельности: запуская их вручную,
# легко получить results/, где matrix.tsv от одного прогона, а overhead.tsv от
# другого. Такое уже случалось на этом стенде, и по самим файлам расхождение
# не видно. Здесь результаты очищаются целиком, а каждый артефакт помечается
# одним и тем же идентификатором прогона.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

RESULTS=./results
RUN_ID="$(date -u '+%Y%m%dT%H%M%SZ')"

echo "=== Полный прогон, RUN_ID=$RUN_ID ==="
echo

echo "--- 0. очистка прошлых результатов ---"
rm -rf "$RESULTS" bin
mkdir -p "$RESULTS"
echo "$RUN_ID" > "$RESULTS/RUN_ID"

echo "--- 1. сборка ---"
./build.sh || { echo "сборка не удалась"; exit 1; }

echo
echo "--- 2. раскладка кучи ---"
./dump-layout.sh > /dev/null || { echo "dump-layout не удался"; exit 1; }

echo "--- 2b. размер арены (проверка объяснения порога) ---"
if [ -x bin/arena-info ]; then
  ./bin/arena-info > "$RESULTS/arena-info.txt"
fi

echo
echo "--- 3. матрица детекта ---"
./run-matrix.sh || { echo "матрица не построена — результаты недостоверны"; exit 1; }

echo
echo "--- 4. накладные расходы ---"
ITER="${ITER:-20000000}" REPEATS="${REPEATS:-5}" ./bench.sh > "$RESULTS/overhead.tsv" \
  || { echo "замер не удался"; exit 1; }

# Помечаем идентификатором прогона КАЖДЫЙ артефакт, включая сырые выводы ячеек:
# именно к ним обращаются, когда перепроверяют сводку, и они тоже не должны
# смешиваться между запусками.
for f in "$RESULTS"/*.tsv "$RESULTS"/*.txt "$RESULTS"/raw/*.txt; do
  [ -f "$f" ] || continue
  case "$(basename "$f")" in
    RUN_ID|COMPLETE) continue ;;
  esac
  printf '# run_id=%s\n' "$RUN_ID" >> "$f"
done

# Маркер завершения ставится ТОЛЬКО здесь, после всех успешных шагов. Наличие
# results/ без него означает прерванный или упавший прогон — такие данные
# использовать нельзя, даже если файлы на вид заполнены.
{
  printf 'run_id=%s\n' "$RUN_ID"
  printf 'завершён: %s\n' "$(date -u '+%Y-%m-%d %H:%M UTC')"
  printf 'все шаги (сборка, раскладка, матрица, замеры) выполнены успешно\n'
} > "$RESULTS/COMPLETE"

echo
echo "=== Готово. RUN_ID=$RUN_ID ==="
echo "Артефакты в $RESULTS/ (включая raw/) помечены этим идентификатором."
echo "Маркер успешного завершения: $RESULTS/COMPLETE"
