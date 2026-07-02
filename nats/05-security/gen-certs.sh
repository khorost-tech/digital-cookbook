#!/usr/bin/env bash
# ДЕМО-СКРИПТ: генерирует самоподписанный демо-CA и пары сервер/клиент cert+key
# для mTLS-стенда 05-security. НЕ ДЛЯ ПРОДАКШНА — приватные ключи учебные,
# срок жизни намеренно короткий, CA не защищён паролем.
#
# Использование:
#   bash gen-certs.sh
#
# Результат — каталог tls/:
#   ca.pem                  демо-CA (можно коммитить, с явной пометкой)
#   server-cert.pem/-key.pem   серверный сертификат (nats-server), SAN: localhost, nats
#   client-cert.pem/-key.pem   клиентский сертификат (mTLS, nats CLI)
#
# tls/*.pem, кроме ca.pem, в .gitignore — приватные ключи сервера/клиента не коммитятся.

set -euo pipefail

# На Git Bash/MSYS (Windows) автоконвертация путей ломает строки вида /CN=... —
# отключаем её для этого скрипта. На Linux/macOS переменная не используется.
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL="*"

cd "$(dirname "$0")"
mkdir -p tls
cd tls

DAYS=30
SUBJ_CA="/C=XX/O=khorost.tech demo/OU=digital-cookbook/CN=nats-05-security-demo-CA"
SUBJ_SERVER="/C=XX/O=khorost.tech demo/OU=digital-cookbook/CN=nats-server"
SUBJ_CLIENT="/C=XX/O=khorost.tech demo/OU=digital-cookbook/CN=nats-client-demo"

echo "== 1/3: демо-CA (самоподписанный, только для этого стенда) =="
openssl req -x509 -newkey ed25519 -nodes \
  -days "$DAYS" \
  -subj "$SUBJ_CA" \
  -keyout ca-key.pem -out ca.pem

echo "== 2/3: серверный сертификат (SAN: localhost, nats, 127.0.0.1) =="
openssl req -newkey ed25519 -nodes \
  -subj "$SUBJ_SERVER" \
  -keyout server-key.pem -out server.csr

cat > server-ext.cnf <<EOF
subjectAltName = DNS:localhost, DNS:nats, IP:127.0.0.1
extendedKeyUsage = serverAuth
EOF

openssl x509 -req -in server.csr \
  -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -days "$DAYS" -extfile server-ext.cnf \
  -out server-cert.pem

echo "== 3/3: клиентский сертификат (mTLS) =="
openssl req -newkey ed25519 -nodes \
  -subj "$SUBJ_CLIENT" \
  -keyout client-key.pem -out client.csr

cat > client-ext.cnf <<EOF
extendedKeyUsage = clientAuth
EOF

openssl x509 -req -in client.csr \
  -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
  -days "$DAYS" -extfile client-ext.cnf \
  -out client-cert.pem

rm -f server.csr client.csr server-ext.cnf client-ext.cnf ca.srl

echo
echo "Готово. Демо-сертификаты в tls/:"
ls -1 *.pem
echo
echo "ВНИМАНИЕ: ca-key.pem, server-key.pem, client-key.pem — демо-приватные ключи."
echo "Они в .gitignore и коммититься не должны. Коммитится (по желанию) только ca.pem"
echo "с явной пометкой 'демо-CA, не для продакшна'."
