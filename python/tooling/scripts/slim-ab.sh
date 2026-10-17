#!/usr/bin/env bash
# Сравнение двух вариантов slim на ОДНОЙ базе и одном хосте:
#   slim-copy  — колёса копируются в образ (COPY), удаляются в том же RUN,
#                где установка (прежний images/slim.Dockerfile);
#   slim       — колёса подключаются к RUN через BuildKit (--mount=type=bind)
#                и в образ не попадают (текущий images/slim.Dockerfile).
# Плюс multistage как ориентир.
#
# Зачем отдельный сценарий. Основная матрица (run-images.sh) снята со
# slim-copy, и её строка slim включает слой с архивами колёс. Пересъёмка всей
# матрицы на другом хосте сдвинула бы все шесть строк разом (база, версия
# Docker), а вопрос здесь один: сколько весит слой, который COPY оставляет.
#
# Размеры снимаются ПОСЛЕ первого запуска контейнера. Сборка под чужую
# платформу (amd64 на arm64-хосте) не распаковывает слои, и до запуска
# docker history показывает 0 у собственных слоёв образа.
set -euo pipefail
cd "$(dirname "$0")/.."

PLATFORM="${PLATFORM:-linux/amd64}"
out=fixtures/images-slim-ab.txt
old="$(mktemp)"
trap 'rm -f "$old"' EXIT
# Прежний вариант восстанавливается из истории, чтобы не держать в стенде
# Dockerfile, который учит неверному.
git show 293eda9e:python/tooling/images/slim.Dockerfile > "$old" 2>/dev/null || {
  echo "нет коммита с прежним slim.Dockerfile" >&2; exit 1; }

build() { docker build -q --platform "$PLATFORM" -f "$2" -t "py-tooling/$1:ab" . >/dev/null; }
build slim-copy "$old"
build slim images/slim.Dockerfile
build multistage images/multistage.Dockerfile

{
  echo "# slim: COPY + rm в том же RUN против подключения через BuildKit"
  echo "# платформа: $PLATFORM, docker $(docker version --format '{{.Server.Version}}')"
  echo "# база python:3.14-slim: $(docker image inspect -f '{{index .RepoDigests 0}}' python:3.14-slim 2>/dev/null || echo '?')"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
  for s in slim-copy slim multistage; do
    t="py-tooling/$s:ab"
    before="$(docker image inspect -f '{{.Size}}' "$t")"
    docker create --platform "$PLATFORM" --name "ab-$s" "$t" >/dev/null
    docker start "ab-$s" >/dev/null 2>&1 || true
    docker rm -f "ab-$s" >/dev/null
    echo "# cmd: docker history --human=false --format '{{.Size}}\t{{.CreatedBy}}' $t"
    unp="$(docker history --human=false --format '{{.Size}}' "$t" | awk '{s+=$1} END{print s}')"
    # После распаковки inspect на этом хосте отдаёт сумму сжатого и
    # распакованного (как docker image ls), до неё — только сжатый, если
    # слои ещё не распакованы. Сжатый берётся разностью, а «до» пишется рядом:
    # где распаковки ещё не было, оба способа обязаны совпасть.
    after="$(docker image inspect -f '{{.Size}}' "$t")"
    comp=$(( after - unp ))
    wheels="$(docker history --human=false --format '{{.Size}}\t{{.CreatedBy}}' "$t" | awk -F'\t' '/\/wheels/ && /COPY/ {print $1}')"
    echo "{\"strategy\": \"$s\", \"compressed_bytes\": $comp, \"unpacked_bytes\": $unp, \"wheels_layer_bytes\": ${wheels:-0}, \"inspect_before\": $before, \"inspect_after\": $after, \"image_ls\": \"$(docker image ls --format '{{.Size}}' "$t")\"}"
  done
} > "$out"
cat "$out"
