#!/usr/bin/env bash
# Прогон четырёх сканеров по всем целям. Результаты — сырые JSON в results/<метка>/.
#
# Скрипт НИЧЕГО не интерпретирует: разбор и сводная таблица — задача analyze.sh.
# Разделение намеренное: сырые артефакты можно перепроверить руками, если сводка
# покажется неправдоподобной.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

# shellcheck disable=SC1091
source ./scanners.env
# shellcheck disable=SC1091
source ./lib.sh
# shellcheck disable=SC1091
source ./targets.env

RESULTS_DIR="./results"
mkdir -p "$RESULTS_DIR"

log "Начало прогона. Целей: ${#TARGETS[@]}"

# Контрольная пара имеет смысл только если бинарник в обеих целях ОДИН И ТОТ ЖЕ.
# Это утверждение проверяется, а не декларируется: если хеши разойдутся, вывод
# «разница только в имени пакета» станет неверным.
verify_control_pair() {
  local a b
  a="$(docker run --rm --entrypoint sh ui-inv-deb-canonical:local -c 'sha256sum /usr/bin/ffmpeg' 2>/dev/null | cut -d' ' -f1)"
  b="$(docker run --rm --entrypoint sh ui-inv-deb-renamed:local   -c 'sha256sum /usr/bin/ffmpeg' 2>/dev/null | cut -d' ' -f1)"
  {
    echo "# Контроль: идентичность бинарника в паре deb-canonical / deb-renamed"
    echo "deb-canonical /usr/bin/ffmpeg sha256: ${a:-НЕ ПОЛУЧЕН}"
    echo "deb-renamed   /usr/bin/ffmpeg sha256: ${b:-НЕ ПОЛУЧЕН}"
  } > "$RESULTS_DIR/control-binary-identity.txt"

  if [ -n "$a" ] && [ "$a" = "$b" ]; then
    echo "ВЕРДИКТ: бинарник идентичен — сравнение корректно" >> "$RESULTS_DIR/control-binary-identity.txt"
    log "Контроль: бинарник в паре идентичен (sha256 ${a:0:16})"
    return 0
  fi

  echo "ВЕРДИКТ: РАСХОЖДЕНИЕ — вывод об имени пакета делать НЕЛЬЗЯ" >> "$RESULTS_DIR/control-binary-identity.txt"
  log "ОСТАНОВКА: бинарники в контрольной паре РАЗЛИЧАЮТСЯ"
  log "  deb-canonical: ${a:-не получен}"
  log "  deb-renamed:   ${b:-не получен}"
  log "  Прогон прерван: сводка на таких целях была бы недостоверной."
  return 1
}

# Прерываемся до сканирования: собрать красивые таблицы на некорректных целях
# хуже, чем не собрать их вовсе — числа уйдут в статью и будут выглядеть
# доказательством того, чего эксперимент не показывал.
if ! verify_control_pair; then
  exit 1
fi

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r label image how <<<"$entry"
  out="$RESULTS_DIR/$label"
  mkdir -p "$out"
  : > "$out/errors.txt"

  log "=== цель '$label' ($image) — $how"

  # Обязательная предпосылка: FFmpeg должен физически присутствовать в образе.
  # Без этого эксперимент бессмыслен — нельзя показывать, что сканер «не нашёл»
  # то, чего в образе нет.
  log "  [0/5] проверяю, что FFmpeg физически есть в образе"
  if docker run --rm --entrypoint sh "$image" -c \
      'command -v ffmpeg >/dev/null 2>&1 || ls /opt/vendored/bin/ffmpeg >/dev/null 2>&1 || ls /usr/lib/jellyfin-ffmpeg/ffmpeg >/dev/null 2>&1' \
      >/dev/null 2>&1; then
    echo "present" > "$out/ffmpeg-present.txt"
    log "        FFmpeg в образе есть"
  else
    echo "NOT FOUND" > "$out/ffmpeg-present.txt"
    echo "ffmpeg физически не найден в образе — цель некорректна" >> "$out/errors.txt"
    log "        ВНИМАНИЕ: FFmpeg в образе не найден"
  fi

  log "  [1/5] syft (SBOM)"
  run_syft "$image" "$out/syft.json" 2>>"$out/errors.txt" || \
    echo "syft завершился с ошибкой" >> "$out/errors.txt"
  # Пустой SBOM нельзя трактовать как «компонентов нет» — на нём строится вся
  # таблица инвентаризации.
  if ! jq -e '.artifacts' "$out/syft.json" >/dev/null 2>&1; then
    echo "ВНИМАНИЕ: syft.json без .artifacts — результат недостоверен" >> "$out/errors.txt"
    log "        ВНИМАНИЕ: syft не вернул валидный SBOM"
  fi

  log "  [2/5] grype (уязвимости по образу)"
  run_grype "$image" "$out/grype.json" 2>>"$out/errors.txt" || \
    echo "grype завершился с ошибкой" >> "$out/errors.txt"
  # Пустой или невалидный артефакт нельзя молча трактовать как «уязвимостей нет»:
  # чаще всего это сорвавшаяся загрузка базы ("database does not exist").
  if ! jq -e '.matches' "$out/grype.json" >/dev/null 2>&1; then
    echo "ВНИМАНИЕ: grype.json без .matches — результат недостоверен (проверьте загрузку БД)" >> "$out/errors.txt"
    log "        ВНИМАНИЕ: grype не вернул валидный результат"
  fi

  log "  [3/5] trivy (уязвимости + инвентаризация)"
  run_trivy "$image" "$out/trivy.json" 2>>"$out/errors.txt" || \
    echo "trivy завершился с ошибкой" >> "$out/errors.txt"
  if ! jq -e '.Results' "$out/trivy.json" >/dev/null 2>&1; then
    echo "ВНИМАНИЕ: trivy.json без .Results — результат недостоверен" >> "$out/errors.txt"
    log "        ВНИМАНИЕ: trivy не вернул валидный результат"
  fi

  log "  [4/5] osv-scanner v2 (scan image — по образу, как и остальные)"
  run_osv "$image" "$out/osv.json" 2>>"$out/errors.txt" || \
    echo "osv-scanner завершился с ошибкой" >> "$out/errors.txt"

  # SBOM в SPDX сохраняем отдельно: переносимый формат для внешних потребителей.
  # В сканировании уязвимостей он НЕ участвует — osv-scanner v2 работает по образу.
  run_syft_spdx "$image" "$out/syft-spdx.json" 2>>"$out/errors.txt" || \
    echo "syft (spdx) завершился с ошибкой" >> "$out/errors.txt"
  if ! jq -e '.spdxVersion' "$out/syft-spdx.json" >/dev/null 2>&1; then
    echo "ВНИМАНИЕ: syft-spdx.json без .spdxVersion — экспорт недостоверен" >> "$out/errors.txt"
    log "        ВНИМАНИЕ: SPDX-экспорт невалиден"
  fi

  log "  [5/5] цель '$label' готова"
done

log "Прогон завершён. Результаты в $RESULTS_DIR/"
