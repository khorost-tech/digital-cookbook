#!/usr/bin/env bash
# Обёртки над сканерами. Все четыре запускаются КОНТЕЙНЕРАМИ с пиновыми версиями:
# так эксперимент воспроизводится без установки инструментов в систему и без
# риска, что у читателя окажется другая версия.
#
# Сканеры образов (syft/grype/trivy) читают образ из локального docker-демона,
# поэтому им пробрасывается сокет. Это осознанный компромисс стенда: доступ к
# docker.sock равносилен доступу к хосту, и в проде так сканеры не запускают —
# там берут образ из реестра по ссылке. Здесь цели собираются локально.

set -euo pipefail

log() {
  printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2
}

# Кэш баз уязвимостей. Без него grype и trivy качают базу при КАЖДОМ запуске
# контейнера (--rm не оставляет состояния), а это и медленно, и ненадёжно:
# сорвавшаяся загрузка даёт "database does not exist" и пустой результат,
# который легко принять за «уязвимостей не найдено».
CACHE_DIR="$(pwd)/.cache"
mkdir -p "$CACHE_DIR/grype" "$CACHE_DIR/trivy"

# SBOM образа. $1 — образ, $2 — выходной файл (формат syft-json).
run_syft() {
  local image="$1" out="$2"
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "$SYFT_IMAGE" "docker:$image" -o syft-json > "$out"
}

# Уязвимости по образу. $1 — образ, $2 — выходной файл.
run_grype() {
  local image="$1" out="$2"
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$CACHE_DIR/grype:/cache" -e GRYPE_DB_CACHE_DIR=/cache \
    "$GRYPE_IMAGE" "docker:$image" -o json > "$out"
}

# Уязвимости + инвентаризация. $1 — образ, $2 — выходной файл.
run_trivy() {
  local image="$1" out="$2"
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$CACHE_DIR/trivy:/root/.cache/trivy" \
    "$TRIVY_IMAGE" image --quiet --format json "$image" > "$out"
}

# SBOM в формате SPDX — пригодится для внешних потребителей и для сверки:
# это тот же прогон syft, но в переносимом формате.
# $1 — образ, $2 — выходной файл.
run_syft_spdx() {
  local image="$1" out="$2"
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "$SYFT_IMAGE" "docker:$image" -o spdx-json > "$out"
}

# Скачивает osv-scanner нужной версии, если его ещё нет, и проверяет sha256.
# Без проверки суммы «воспроизводимый стенд» превращается в «что скачалось».
ensure_osv() {
  if [ -x "$OSV_BIN" ]; then
    return 0
  fi
  mkdir -p "$(dirname "$OSV_BIN")"
  log "  скачиваю osv-scanner ${OSV_VERSION}"
  # HTTPS_PROXY подхватывается curl автоматически, если задан в окружении.
  curl -sSL --max-time 300 -o "$OSV_BIN" "$OSV_URL" || {
    log "  не удалось скачать osv-scanner"
    rm -f "$OSV_BIN"
    return 1
  }
  local actual
  actual="$(sha256sum "$OSV_BIN" | cut -d' ' -f1)"
  if [ "$actual" != "$OSV_SHA256" ]; then
    log "  КОНТРОЛЬНАЯ СУММА НЕ СОВПАЛА: ожидалась $OSV_SHA256, получена $actual"
    rm -f "$OSV_BIN"
    return 1
  fi
  chmod +x "$OSV_BIN"
  return 0
}

# osv-scanner v2 умеет сканировать образ напрямую — в отличие от ветки 1.x,
# которая работала только по SBOM и манифестам. Поэтому сравнение с grype/trivy
# идёт на равных условиях: все три получают один и тот же образ.
# $1 — образ, $2 — выходной файл.
run_osv() {
  local image="$1" out="$2"
  ensure_osv || { echo '{}' > "$out"; return 1; }

  local rc=0
  "$OSV_BIN" scan image --format json "$image" > "$out" 2>/dev/null || rc=$?

  # У osv-scanner ненулевой код возврата НЕ равен ошибке: 1 означает «уязвимости
  # найдены». Глушить всё подряд через `|| true` нельзя — настоящий сбой тогда
  # неотличим от чистого результата и превращается в ложный ноль.
  case "$rc" in
    0|1) ;;                       # 0 — чисто, 1 — найдены уязвимости
    *) log "  osv-scanner завершился с кодом $rc (это ошибка, а не находки)"
       return "$rc" ;;
  esac

  # Структурная проверка: без ключа results результат недостоверен.
  if ! jq -e '.results' "$out" >/dev/null 2>&1; then
    log "  osv-scanner вернул JSON без .results — результат недостоверен"
    return 1
  fi
  return 0
}
