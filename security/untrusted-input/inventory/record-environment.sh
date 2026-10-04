#!/usr/bin/env bash
# Фиксация окружения прогона: без этого числа в статье невоспроизводимы.
# Пишет results/environment.txt — версии сканеров, digest'ы целей, дату, версию docker.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

# shellcheck disable=SC1091
source ./scanners.env
# shellcheck disable=SC1091
source ./targets.env

# Тот же каталог кэша, что использует lib.sh — иначе запросим статус пустой базы.
CACHE_DIR="$(pwd)/.cache"

OUT="./results/environment.txt"
mkdir -p ./results
: > "$OUT"

{
  echo "# Окружение прогона"
  echo
  echo "Дата снимка: $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo "Docker: $(docker --version)"
  echo
  echo "## Версии сканеров (из самих контейнеров)"
} >> "$OUT"

printf '  syft:        %s\n' "$SYFT_IMAGE" >> "$OUT"
docker run --rm "$SYFT_IMAGE" version 2>/dev/null | sed 's/^/    /' >> "$OUT" || true
printf '  grype:       %s\n' "$GRYPE_IMAGE" >> "$OUT"
docker run --rm "$GRYPE_IMAGE" version 2>/dev/null | sed 's/^/    /' >> "$OUT" || true
printf '  trivy:       %s\n' "$TRIVY_IMAGE" >> "$OUT"
docker run --rm "$TRIVY_IMAGE" --version 2>/dev/null | sed 's/^/    /' >> "$OUT" || true
# osv-scanner ставится бинарником, а не контейнером — фиксируем версию, источник
# и контрольную сумму, иначе прогон невоспроизводим.
{
  printf '  osv-scanner: бинарник v%s\n' "$OSV_VERSION"
  printf '    источник: %s\n' "$OSV_URL"
  printf '    ожидаемый sha256: %s\n' "$OSV_SHA256"
} >> "$OUT"
if [ -x "$OSV_BIN" ]; then
  printf '    фактический sha256: %s\n' "$(sha256sum "$OSV_BIN" | cut -d' ' -f1)" >> "$OUT"
  "$OSV_BIN" --version 2>/dev/null | sed 's/^/    /' >> "$OUT" || true
else
  printf '    ВНИМАНИЕ: бинарник %s отсутствует\n' "$OSV_BIN" >> "$OUT"
fi

# Состояние баз уязвимостей. Версии сканеров закреплены, а базы — живые:
# это главная причина, по которой ЧИСЛА прогона воспроизводятся лишь частично,
# даже если инструменты те же. Фиксируем, что было на момент снимка.
{
  echo
  echo "## Состояние баз уязвимостей на момент прогона"
  echo "  (версии сканеров пиновые, но их БАЗЫ обновляются — числа привязаны к этому состоянию)"
} >> "$OUT"

docker run --rm -v "$CACHE_DIR/grype:/cache" -e GRYPE_DB_CACHE_DIR=/cache \
  "$GRYPE_IMAGE" db status 2>/dev/null | sed 's/^/    grype: /' >> "$OUT" || \
  echo "    grype: статус БД получить не удалось" >> "$OUT"

docker run --rm -v "$CACHE_DIR/trivy:/root/.cache/trivy" \
  "$TRIVY_IMAGE" version --format json 2>/dev/null | sed 's/^/    trivy: /' >> "$OUT" || \
  echo "    trivy: метаданные БД получить не удалось" >> "$OUT"

echo "    osv-scanner: обращается к live-данным OSV.dev, локальной базы нет" >> "$OUT"

{
  echo
  echo "## Целевые образы"
} >> "$OUT"

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r label image how <<<"$entry"
  digest="$(docker inspect --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}(локальная сборка, digest отсутствует){{end}}' "$image" 2>/dev/null || echo '(образ не найден)')"
  size="$(docker inspect --format '{{.Size}}' "$image" 2>/dev/null || echo '?')"
  {
    echo "  [$label] $image"
    echo "    способ поставки FFmpeg: $how"
    echo "    digest: $digest"
    echo "    размер: $size байт"
  } >> "$OUT"
done

cat "$OUT"
