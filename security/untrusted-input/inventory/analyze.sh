#!/usr/bin/env bash
# Разбор сырых JSON и сводные таблицы.
#
# Принцип: ни одно число не берётся «на глаз» — всё считается из артефактов
# прогона. Сырые JSON лежат рядом, любое утверждение можно перепроверить руками.
#
# Ключевое разделение, без которого выводы получаются неверными:
#   ИНВЕНТАРИЗАЦИЯ — видит ли инструмент компонент вообще (syft/SBOM);
#   СОПОСТАВЛЕНИЕ  — связал ли он этот компонент с уязвимостями (grype/trivy/osv).
# Компонент можно прекрасно видеть и при этом не знать ни одной его уязвимости.
#
# Второе разделение — находки против уникальных идентификаторов: один CVE может
# дать несколько записей (разные пакеты, разные матчеры), поэтому «находки» и
# «уникальные CVE» считаются отдельно и называются по-разному.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

# shellcheck disable=SC1091
source ./targets.env

RESULTS_DIR="./results"
CVE="CVE-2026-8461"
# Что считаем «компонентом FFmpeg»: сам ffmpeg (в т.ч. переименованный вендором)
# или любая библиотека libav*.
FFMPEG_RE="ffmpeg|libav(codec|format|util|filter|device|resample)"

# Читает JSON-артефакт независимо от того, сжат он или нет: сырые результаты
# прогона лежат в репозитории в .gz, а свежий прогон кладёт обычный .json.
read_artifact() {
  local f="$1"
  if [ -f "$f" ]; then cat "$f"
  elif [ -f "$f.gz" ]; then zcat "$f.gz"
  fi
}

# Есть ли артефакт в любом из двух видов.
has_artifact() {
  [ -f "$1" ] || [ -f "$1.gz" ]
}

j() { read_artifact "$2" | jq -r "$1" 2>/dev/null || echo ""; }

emit() { printf '%s\n' "$*"; }

emit "# Кто видит FFmpeg внутри образа"
emit ""
emit "Окружение прогона: \`results/environment.txt\`. Сырые артефакты: \`results/<цель>/\`."
emit ""

# --- Таблица 1: инвентаризация ---
emit "## 1. Инвентаризация: виден ли компонент"
emit ""
emit "| Цель | Способ поставки | FFmpeg физически в образе | syft: найденные компоненты FFmpeg | тип записи | всего компонентов |"
emit "|---|---|---|---|---|---|"

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r label _ how <<<"$entry"
  dir="$RESULTS_DIR/$label"
  [ -d "$dir" ] || continue

  present="$(cat "$dir/ffmpeg-present.txt" 2>/dev/null || echo '?')"
  [ "$present" = "present" ] && present="да" || present="НЕТ"

  comps="$(j "[.artifacts[] | select(.name | test(\"$FFMPEG_RE\"; \"i\")) | \"\(.name)@\(.version)\"] | unique | join(\", \")" "$dir/syft.json")"
  types="$(j "[.artifacts[] | select(.name | test(\"$FFMPEG_RE\"; \"i\")) | .type] | unique | join(\", \")" "$dir/syft.json")"
  total="$(j '[.artifacts[]] | length' "$dir/syft.json")"

  [ -n "$comps" ] || comps="**не найден**"
  [ -n "$types" ] || types="—"

  emit "| \`$label\` | $how | $present | $comps | $types | $total |"
done

# --- Таблица 2: сопоставление с уязвимостями ---
emit ""
emit "## 2. Сопоставление: связан ли компонент с уязвимостями"
emit ""
emit "«Находки» — записи матчера, а не уникальные уязвимости: один идентификатор"
emit "может дать несколько записей по разным пакетам. Поэтому отдельно приведено"
emit "число уникальных CVE/GHSA, отнесённых к компоненту FFmpeg."
emit ""
emit "| Цель | grype: находки / по FFmpeg / уник. ID по FFmpeg | trivy: находки / по FFmpeg / уник. ID по FFmpeg | osv: находки / по FFmpeg |"
emit "|---|---|---|---|"

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r label _ _ <<<"$entry"
  dir="$RESULTS_DIR/$label"
  [ -d "$dir" ] || continue

  g_tot="$(j '[.matches[]] | length' "$dir/grype.json")"
  g_ff="$(j "[.matches[] | select(.artifact.name | test(\"$FFMPEG_RE\"; \"i\"))] | length" "$dir/grype.json")"
  g_uniq="$(j "[.matches[] | select(.artifact.name | test(\"$FFMPEG_RE\"; \"i\")) | .vulnerability.id] | unique | length" "$dir/grype.json")"

  t_tot="$(j '[.Results[]? | .Vulnerabilities[]?] | length' "$dir/trivy.json")"
  t_ff="$(j "[.Results[]? | .Vulnerabilities[]? | select(.PkgName | test(\"$FFMPEG_RE\"; \"i\"))] | length" "$dir/trivy.json")"
  t_uniq="$(j "[.Results[]? | .Vulnerabilities[]? | select(.PkgName | test(\"$FFMPEG_RE\"; \"i\")) | .VulnerabilityID] | unique | length" "$dir/trivy.json")"

  o_tot="$(j '[.results[]? | .packages[]? | .vulnerabilities[]?] | length' "$dir/osv.json")"
  o_ff="$(j "[.results[]? | .packages[]? | select(.package.name | test(\"$FFMPEG_RE\"; \"i\")) | .vulnerabilities[]?] | length" "$dir/osv.json")"

  emit "| \`$label\` | ${g_tot:-0} / **${g_ff:-0}** / ${g_uniq:-0} | ${t_tot:-0} / **${t_ff:-0}** / ${t_uniq:-0} | ${o_tot:-0} / **${o_ff:-0}** |"
done

# --- Таблица 2b: привязан ли целевой CVE именно к FFmpeg ---
emit ""
emit "### Отнесён ли $CVE к компоненту FFmpeg"
emit ""
emit "Проверка структурная: идентификатор уязвимости и имя пакета берутся из одной"
emit "записи. Поиск строки по всему JSON этого не доказывал бы — CVE мог бы"
emit "упоминаться в связанных данных другого пакета."
emit ""
emit "| Цель | grype: к каким пакетам отнесён | trivy: к каким пакетам отнесён |"
emit "|---|---|---|"

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r label _ _ <<<"$entry"
  dir="$RESULTS_DIR/$label"
  [ -d "$dir" ] || continue

  g_pkgs="$(j "[.matches[] | select(.vulnerability.id == \"$CVE\") | \"\(.artifact.name)@\(.artifact.version)\"] | unique | join(\", \")" "$dir/grype.json")"
  t_pkgs="$(j "[.Results[]? | .Vulnerabilities[]? | select(.VulnerabilityID == \"$CVE\") | \"\(.PkgName)@\(.InstalledVersion)\"] | unique | join(\", \")" "$dir/trivy.json")"

  [ -n "$g_pkgs" ] || g_pkgs="—"
  [ -n "$t_pkgs" ] || t_pkgs="—"

  emit "| \`$label\` | $g_pkgs | $t_pkgs |"
done

# --- Как syft идентифицирует компонент ---
emit ""
emit "## 3. Чем компонент представлен в SBOM"
emit ""
emit "Идентичность компонента для сопоставления складывается из имени, версии,"
emit "типа пакета, namespace дистрибутива и purl/CPE — а не из содержимого файла."
emit ""
emit "| Цель | name | version | type | purl |"
emit "|---|---|---|---|---|"

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r label _ _ <<<"$entry"
  dir="$RESULTS_DIR/$label"
  # Артефакт может быть и сжатым — проверяем оба варианта.
  has_artifact "$dir/syft.json" || continue
  j "[.artifacts[] | select(.name | test(\"ffmpeg\"; \"i\"))] | .[0:3] | .[] | \"| \`$label\` | \(.name) | \(.version) | \(.type) | \`\(.purl)\` |\"" "$dir/syft.json"
done

emit ""
emit "## Как читать"
emit ""
emit "- «FFmpeg физически в образе = да» проверено перед сканированием (запуск/наличие"
emit "  бинарника). Если сканер при этом молчит — это слепое пятно инструмента, а не"
emit "  ошибка эксперимента."
emit "- Колонка «находки» показывает, что инструмент отработал и образ увидел: дело не в"
emit "  сломанном прогоне, а в том, что конкретный компонент не попал в его картину мира."
