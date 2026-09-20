#!/usr/bin/env bash
# build.sh <версия> — собирает плагин в контейнере на базе AlmaLinux 8.10
# (glibc 2.28, как у образа Picodata) и раскладывает результат в общий том
# share_dir, откуда кластер его установит. Сборка вне этого образа даёт .so,
# который кластер не загрузит.
#
# Кэш cargo вынесен в именованный том: без него каждая пересборка заново тянет
# и компилирует всё дерево зависимостей.
set -euo pipefail
STAND_DIR="$(cd "$(dirname "$0")/.." && pwd)"
STAND_DIR_DOCKER="$(cd "$(dirname "$0")/.." && (pwd -W 2>/dev/null || pwd))"
export MSYS_NO_PATHCONV=1

VERSION="${1:?usage: build.sh <версия плагина, например 0.1.0>}"

# Версия каталога в share_dir обязана совпадать с версией пакета: реестр
# сервисов внутри библиотеки регистрирует их под CARGO_PKG_VERSION, и если
# положить сборку 0.2.0 в каталог 0.1.0, кластер найдёт манифест, но не найдёт
# сервисов и откажет:
#   Failed to install plugin `near_data:0.1.0`: Plugin partial load
#   (some of services not found: ["near_data_service"])
CARGO_VERSION="$(grep -m1 '^version *= *"' "${STAND_DIR}/plugin/Cargo.toml" \
                 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')"
if [ "$VERSION" != "$CARGO_VERSION" ]; then
    echo "!!! запрошена версия ${VERSION}, а в plugin/Cargo.toml — ${CARGO_VERSION}" >&2
    echo "    поднимите версию в Cargo.toml или собирайте ${CARGO_VERSION}" >&2
    exit 1
fi

docker run --rm \
    -v "${STAND_DIR_DOCKER}/plugin:/src" \
    -v picodata-cargo-registry:/root/.cargo/registry \
    -v picodata-cargo-target:/src/target \
    -w /src picodata-plugin-build:8.10 \
    sh -c "cargo build --release"

# Раскладка в share_dir: <share>/<имя плагина>/<версия>/
docker run --rm \
    -v picodata-share:/share \
    -v picodata-cargo-target:/target \
    -v "${STAND_DIR_DOCKER}/plugin:/src" \
    picodata-plugin-build:8.10 \
    sh -c "mkdir -p /share/near_data/${VERSION}/migrations && \
           cp /target/release/libnear_data.so /share/near_data/${VERSION}/ && \
           cp /src/migrations/*.sql /share/near_data/${VERSION}/migrations/ && \
           sed 's/{{ version }}/${VERSION}/' /src/manifest.yaml.template \
               | sed 's/{% for migration in migrations -%}//; s/{% endfor -%}//; s/- {{ migration }}/- migrations\/0001_init.sql/' \
               > /share/near_data/${VERSION}/manifest.yaml && \
           ls -la /share/near_data/${VERSION}/ && \
           echo '--- manifest ---' && cat /share/near_data/${VERSION}/manifest.yaml"

echo "ok: плагин near_data ${VERSION} разложен в share_dir"
