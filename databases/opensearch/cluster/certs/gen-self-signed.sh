#!/usr/bin/env bash
# gen-self-signed.sh — генерирует demo-CA и сертификаты (node + admin) для стенда
# opensearch/cluster без зависимости от Vault.
#
# В отличие от пути с Vault PKI, описанного в статье (Vault отдаёт ключ в PKCS#1,
# и нужен отдельный handler-конвертация в PKCS#8 через `openssl pkcs8 -topk8`),
# здесь ключи сразу генерируются в PKCS#8: OpenSSL 3.x пишет ключи в этом формате
# по умолчанию при использовании `-genpkey`, поэтому промежуточный шаг конвертации
# не нужен — файлы `*-key.pem` уже готовы к использованию security-плагином OpenSearch.
#
# Использование:
#   cd opensearch/cluster/certs
#   ./gen-self-signed.sh
#
# Результат — каталог out/ с файлами:
#   root-ca.pem, root-ca-key.pem       — demo CA (самоподписанный)
#   node.pem,    node-key.pem          — сертификат ноды (общий для всех 3 нод стенда;
#                                        в проде — отдельный сертификат на ноду с её SAN)
#   admin.pem,   admin-key.pem         — admin-сертификат для securityadmin.sh
#
# ВНИМАНИЕ: это demo-сертификаты для локального/тестового стенда. Не использовать
# как есть за пределами тестового окружения — см. README.md, раздел "Demo credentials".

set -euo pipefail

# На Git Bash / MSYS (Windows) пути вида "/C=RU/L=..." в -subj иначе трактуются
# как POSIX-пути и портятся автоконвертацией. На Linux/macOS переменная ни на что
# не влияет — установка безопасна в любом окружении.
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL="*"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${SCRIPT_DIR}/out"
DAYS_CA=3650
DAYS_CERT=825

# DN admin-сертификата должен совпадать с opensearch_admin_dn в group_vars/all.yml
# и с plugins.security.authcz.admin_dn в конфигурации security-плагина.
ADMIN_SUBJECT="/C=RU/L=Demo/O=khorost.tech/OU=digital-cookbook/CN=admin"
NODE_SUBJECT="/C=RU/L=Demo/O=khorost.tech/OU=digital-cookbook/CN=os-node"
CA_SUBJECT="/C=RU/L=Demo/O=khorost.tech/OU=digital-cookbook/CN=demo-root-ca"

# SAN нод: hostnames + IP стенда из inventory/hosts.ini (os-node-1..3, 10.0.0.11..13).
NODE_SAN="subjectAltName=DNS:os-node-1,DNS:os-node-2,DNS:os-node-3,DNS:localhost,IP:10.0.0.11,IP:10.0.0.12,IP:10.0.0.13,IP:127.0.0.1"

mkdir -p "${OUT_DIR}"
cd "${OUT_DIR}"

echo "==> 1/4: demo root CA"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out root-ca-key.pem
openssl req -x509 -new -key root-ca-key.pem -sha256 -days "${DAYS_CA}" \
  -subj "${CA_SUBJECT}" -out root-ca.pem

echo "==> 2/4: node cert (используется всеми 3 нодами стенда; transport 9300 + REST 9200)"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out node-key.pem
openssl req -new -key node-key.pem -subj "${NODE_SUBJECT}" -out node.csr
printf '%s\nextendedKeyUsage=serverAuth,clientAuth\n' "${NODE_SAN}" > node.ext
openssl x509 -req -in node.csr -CA root-ca.pem -CAkey root-ca-key.pem -CAcreateserial \
  -days "${DAYS_CERT}" -sha256 -extfile node.ext \
  -out node.pem
rm -f node.csr node.ext

echo "==> 3/4: admin cert (для securityadmin.sh — DN должен совпадать с admin_dn в securityconfig)"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out admin-key.pem
openssl req -new -key admin-key.pem -subj "${ADMIN_SUBJECT}" -out admin.csr
printf 'extendedKeyUsage=clientAuth\n' > admin.ext
openssl x509 -req -in admin.csr -CA root-ca.pem -CAkey root-ca-key.pem -CAcreateserial \
  -days "${DAYS_CERT}" -sha256 -extfile admin.ext \
  -out admin.pem
rm -f admin.csr admin.ext

echo "==> 4/4: права доступа на приватные ключи"
chmod 0600 root-ca-key.pem node-key.pem admin-key.pem

echo
echo "Готово. Сертификаты и ключи (уже PKCS#8, конвертация не нужна) — в ${OUT_DIR}/"
echo "Проверить формат ключа:"
echo "  head -1 ${OUT_DIR}/node-key.pem   # должно быть: -----BEGIN PRIVATE KEY-----  (не 'RSA PRIVATE KEY')"
echo
echo "DN admin-сертификата (сверить с securityconfig/*.yml и group_vars/all.yml):"
openssl x509 -in admin.pem -noout -subject
