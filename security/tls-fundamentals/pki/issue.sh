#!/usr/bin/env bash
# Выпуск сертификатов стенда — В ЗАКРЕПЛЁННОМ ОБРАЗЕ.
#
# ПОЧЕМУ НЕ НА МАШИНЕ. README обещал, что нужны только Docker и Go, а
# выпуск звал хостовый openssl и хостовый python. У автора оба нашлись, и
# прогон «проходил целиком»; на чужой машине он падал дважды — на
# отсутствии python и на OpenSSL 3.0, где нет ключа -not_before. Образ с
# нужной версией уже собирался, просто выпуск шёл мимо него.
#
# Ключ -not_before появился в OpenSSL 3.2. Стенду он необходим: без него
# не выпустить ни просроченный сертификат, ни ещё не начавшийся, а на них
# держатся два случая матрицы.
set -uo pipefail

export MSYS_NO_PATHCONV=1

IMAGE="tls-stand-openssl:1"
STAND_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REF="${1:-20260101000000Z}"

command -v docker >/dev/null 2>&1 || {
    echo "нет docker: выпуск сертификатов идёт в образе" >&2
    exit 2
}

# Каталог подключается на ЗАПИСЬ: сюда кладутся выпущенные сертификаты.
docker run --rm \
    -v "$STAND_ROOT:/stand" -w /stand \
    --entrypoint sh "$IMAGE" pki/make-certs.sh "$REF"
