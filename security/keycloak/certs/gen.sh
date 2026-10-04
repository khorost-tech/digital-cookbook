#!/usr/bin/env bash
# Генерация self-signed TLS-сертификата для production-стенда Keycloak
# (docker-compose.prod.yml). Один сертификат используется и nginx-LB (терминация
# клиентского TLS на :8443), и самими репликами Keycloak (KC_HTTPS_CERTIFICATE_FILE,
# nginx re-encrypt'ит трафик на keycloak:8443).
#
# Файлы кладутся в этот же каталог как *.pem и НЕ коммитятся (см. ../.gitignore:
# `certs/*.pem`). Скрипт идемпотентен: повторный запуск перегенерирует пару.
#
# SAN покрывает и внешнее имя (localhost/127.0.0.1 — за LB), и docker-hostname'ы
# реплик (keycloak-1/keycloak-2) — чтобы re-encrypt между nginx и Keycloak проходил
# без ошибок имени, если включить проверку (в стенде proxy_ssl_verify off).
set -euo pipefail

# На Git-Bash/MSYS путь-подобные аргументы (-subj "/C=...") иначе конвертируются
# в Windows-путь. На Linux/WSL переменные безвредны.
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CRT="keycloak.crt.pem"
KEY="keycloak.key.pem"

# Работаем из каталога с относительными именами файлов: это переносимо и между
# native-openssl на Windows (Git-Bash), и openssl на Linux/WSL.
cd "$DIR"

openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout "$KEY" -out "$CRT" \
  -subj "/C=RU/O=khorost.tech/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,DNS:keycloak,DNS:keycloak-1,DNS:keycloak-2,IP:127.0.0.1"

chmod 600 "$KEY"
echo "TLS cert:  $DIR/$CRT"
echo "TLS key:   $DIR/$KEY"
openssl x509 -in "$CRT" -noout -subject -ext subjectAltName
