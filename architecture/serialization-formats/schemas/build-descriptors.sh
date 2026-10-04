#!/usr/bin/env bash
# Пересобирает FileDescriptorSet для каждой версии proto-схемы.
#
# Каждый .proto собирается ОТДЕЛЬНО (не как единый buf-модуль), потому
# что все версии объявляют одно и то же полное имя сообщения
# tech.khorost.serialization.User — общий buf-модуль споткнётся о
# дублирующийся символ. Дескрипторы коммитятся в git: это часть схемы,
# а не сборочный артефакт, и Java-часть (Задача 4) читает те же файлы.
#
# --exclude-source-info: круг ревью 2, находка C1. Без флага сборщик по
# умолчанию кладёт в дескриптор source_code_info — карту строк и
# столбцов исходного .proto-текста (номера строк комментариев, отступы
# и т.п.). Читателю, декодирующему запись, эта карта не нужна ни для
# чего — ровно как убранные раньше строки `option go_package`/
# `option java_package` (см. git log). На схеме стенда это была БОЛЬШАЯ
# часть веса дескриптора (например user_v1.desc: 340 -> 119 байт, то
# есть карта позиций весила 217 байт из 340, 65 % файла) — включать её
# в вес схемы значило бы сравнивать наш артефакт сборки, а не формат.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

IMAGE="bufbuild/buf:1.72.0"
HOST_DIR="$(pwd)"

for proto in *.proto; do
    desc="${proto%.proto}.desc"
    echo "buf build ${proto} -> ${desc}"
    MSYS_NO_PATHCONV=1 docker run --rm \
        -v "${HOST_DIR}:/w" -w /w \
        "${IMAGE}" build "/w/${proto}" \
        --exclude-source-info \
        --as-file-descriptor-set -o "/w/${desc}"
done
