#!/bin/sh
# Выпуск всего набора сертификатов для стенда.
#
# ПОЧЕМУ ЭТОТ СКРИПТ ИСПОЛНЯЕТСЯ В ОБРАЗЕ, А НЕ НА МАШИНЕ.
# Раньше он звал хостовый openssl и хостовый python. У автора оба
# нашлись, и прогон «проходил целиком»; на чужой машине он падал
# дважды — на отсутствии python и на OpenSSL 3.0, где нет ключа
# -not_before. Своё окружение было выдано за свойство стенда.
# Запускать этот файл следует через pki/issue.sh.
#
# ПОЭТОМУ ЖЕ ЗДЕСЬ POSIX-ОБОЛОЧКА. В образе нет bash: ни BASH_SOURCE,
# ни local, ни массивов.
#
# ПОЧЕМУ ДАТЫ ФИКСИРОВАННЫЕ, А НЕ «СЕГОДНЯ ПЛЮС ГОД».
# Случай «просрочен» обязан быть просроченным всегда, а «ещё не начался» —
# всегда будущим. Если считать от текущего дня, через год фикстура начнёт
# показывать другое, и никто не заметит: числа останутся правдоподобными.
# Поэтому опорная дата задаётся явно, а все сроки отсчитываются от неё.
#
# ПОЧЕМУ КАЖДЫЙ СЛУЧАЙ ЛОМАЕТ ОДНО.
# Отказ клиента должен объясняться единственной причиной. Два дефекта в
# одном сертификате делают исход неразличимым: непонятно, на что именно
# клиент отреагировал.
#
# ПОЧЕМУ РАСШИРЕНИЯ ПИШУТСЯ ВО ВРЕМЕННЫЕ ФАЙЛЫ, А НЕ ЧЕРЕЗ ПОДСТАНОВКУ
# ПРОЦЕССА. Подстановка даёт путь вида /dev/fd/63, и нативный OpenSSL под
# Windows открыть его не может — падает на попытке чтения. Обычный файл
# работает везде.
set -euo pipefail

# Git Bash на Windows превращает аргумент вида /CN=... в путь и ломает
# выпуск. На прочих системах переменная просто игнорируется.
export MSYS_NO_PATHCONV=1

cd "$(dirname "$0")"

REF="${1:-20260101000000Z}"          # опорная дата, от неё все сроки
DOMAIN="stand.local"
OTHER_DOMAIN="other.local"

rm -f ./*.pem ./*.key ./*.crt ./*.csr ./*.srl ./*.ext
rm -rf db && mkdir -p db && : > db/index.txt && echo 1000 > db/serial

# Смещение от опорной даты СРЕДСТВАМИ ОБОЛОЧКИ. Здесь стоял вызов
# python — из-за него выпуск не работал там, где python не в пути.
REF_EPOCH=$(date -D '%Y%m%d%H%M%SZ' -d "$REF" +%s)

shift_days() {
    date -d "@$((REF_EPOCH + $1 * 86400))" -u +%Y%m%d%H%M%SZ
}

FAR_PAST=$(shift_days -400)
PAST=$(shift_days -100)
NOW=$(shift_days -1)
SOON=$(shift_days 400)
FAR_FUTURE=$(shift_days 4000)

key() { openssl genrsa -out "$1" 2048 2>/dev/null; }

# --- корневые центры: наш и чужой ------------------------------------
for name in root other-root; do
    key "$name.key"
    openssl req -x509 -new -key "$name.key" -out "$name.pem" \
        -subj "/CN=$name.tls-stand" -not_before "$NOW" -not_after "$FAR_FUTURE" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
done

# --- промежуточный центр, подписан нашим корнем -----------------------
cat > ca.ext <<'EXT'
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
EXT
key intermediate.key
openssl req -new -key intermediate.key -out intermediate.csr \
    -subj "/CN=intermediate.tls-stand" 2>/dev/null
openssl x509 -req -in intermediate.csr -CA root.pem -CAkey root.key \
    -CAcreateserial -out intermediate.pem \
    -not_before "$NOW" -not_after "$FAR_FUTURE" -extfile ca.ext 2>/dev/null

# leaf <имя> <издатель> <ключ издателя> <notBefore> <notAfter> <имя в SAN>
#
# Субъект у всех листов ОДИН И ТОТ ЖЕ. Иначе случай «неверное имя»
# отличался бы от контроля двумя признаками сразу — и субъектом, и
# SAN, — а клиенты смотрят на SAN. Ломать надо ровно то, что
# проверяется.
leaf() {
    name="$1"; ca="$2"; cakey="$3"; nb="$4"; na="$5"; dom="$6"
    cat > "$name.ext" <<EXT
subjectAltName=DNS:$dom
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth,clientAuth
EXT
    key "$name.key"
    openssl req -new -key "$name.key" -out "$name.csr" -subj "/CN=$DOMAIN" 2>/dev/null
    openssl x509 -req -in "$name.csr" -CA "$ca" -CAkey "$cakey" \
        -CAcreateserial -out "$name.pem" \
        -not_before "$nb" -not_after "$na" -extfile "$name.ext" 2>/dev/null
}

# --- листовые сертификаты: по одному дефекту на каждый ----------------
leaf valid          intermediate.pem intermediate.key "$NOW"      "$SOON"       "$DOMAIN"
leaf expired        intermediate.pem intermediate.key "$FAR_PAST" "$PAST"       "$DOMAIN"
leaf not-yet-valid  intermediate.pem intermediate.key "$SOON"     "$FAR_FUTURE" "$DOMAIN"
leaf wrong-san      intermediate.pem intermediate.key "$NOW"      "$SOON"       "$OTHER_DOMAIN"
leaf other-ca       other-root.pem   other-root.key   "$NOW"      "$SOON"       "$DOMAIN"
leaf revoked        intermediate.pem intermediate.key "$NOW"      "$SOON"       "$DOMAIN"

# --- глубокая ветка для проверки порядка внутри цепочки ---------------
#
# Порядок можно сломать, только если промежуточных больше одного. При
# одном переставлять нечего: перестановка листа с промежуточным ломает
# ещё и соответствие ключа первому сертификату, и отказ объяснялся бы
# двумя причинами сразу.
#
# Ветка своя, отдельная от основной: у общего промежуточного ограничение
# длины пути запрещает подписывать другие центры, а менять его — значит
# менять контроль.
cat > ca1.ext <<'EXT'
basicConstraints=critical,CA:TRUE,pathlen:1
keyUsage=critical,keyCertSign,cRLSign
EXT
key deep-ca1.key
openssl req -new -key deep-ca1.key -out deep-ca1.csr \
    -subj "/CN=deep-ca1.tls-stand" 2>/dev/null
openssl x509 -req -in deep-ca1.csr -CA root.pem -CAkey root.key \
    -CAcreateserial -out deep-ca1.pem \
    -not_before "$NOW" -not_after "$FAR_FUTURE" -extfile ca1.ext 2>/dev/null

key deep-ca2.key
openssl req -new -key deep-ca2.key -out deep-ca2.csr \
    -subj "/CN=deep-ca2.tls-stand" 2>/dev/null
openssl x509 -req -in deep-ca2.csr -CA deep-ca1.pem -CAkey deep-ca1.key \
    -CAcreateserial -out deep-ca2.pem \
    -not_before "$NOW" -not_after "$FAR_FUTURE" -extfile ca.ext 2>/dev/null

leaf deep           deep-ca2.pem     deep-ca2.key     "$NOW"      "$SOON"       "$DOMAIN"

# --- список отозванных, подписанный промежуточным ---------------------
#
# Отзыв не живёт в сертификате: сам сертификат у отозванного неотличим от
# контрольного. Состояние ведёт удостоверяющий центр, и клиенту оно
# достаётся отдельным источником. Список нужен, чтобы показать это на
# опыте: с ним и без него ответ на ОДИН И ТОТ ЖЕ сертификат разный.
cat > crl.cnf <<'CNF'
[ca]
default_ca = stand
[stand]
database = db/index.txt
serial = db/serial
default_md = sha256
crl_extensions = crl_ext
default_crl_days = 3650
[crl_ext]
authorityKeyIdentifier=keyid:always
CNF
openssl ca -config crl.cnf -valid revoked.pem \
    -cert intermediate.pem -keyfile intermediate.key \
    -batch -notext 2>/dev/null || true
openssl ca -config crl.cnf -revoke revoked.pem \
    -cert intermediate.pem -keyfile intermediate.key \
    -batch 2>/dev/null || true
openssl ca -config crl.cnf -gencrl -out crl.pem \
    -cert intermediate.pem -keyfile intermediate.key \
    -batch 2>/dev/null || true

# самоподписанный: сам себе издатель, цепочки нет вовсе
key self-signed.key
openssl req -x509 -new -key self-signed.key -out self-signed.pem \
    -subj "/CN=$DOMAIN" -not_before "$NOW" -not_after "$SOON" \
    -addext "subjectAltName=DNS:$DOMAIN" \
    -addext "basicConstraints=critical,CA:FALSE" 2>/dev/null

# --- связки для клиентских сертификатов -------------------------------
#
# Клиент обязан прислать не только свой лист, но и путь к корню: иначе
# сервер не сможет построить цепочку, и ЛЮБОЙ слом клиентского
# сертификата объяснялся бы одинаково — «неизвестный центр». Просроченный
# клиентский сертификат тогда отличался бы от чужого не одним признаком,
# а двумя, и ось меряла бы не то.
cat valid.pem intermediate.pem   > client-valid.pem
cat expired.pem intermediate.pem > client-expired.pem
cat other-ca.pem other-root.pem  > client-other.pem

# Ключи выпускаются внутри образа от суперпользователя, и openssl
# кладёт их с правами 600. Клиенты стенда работают в СВОИХ образах под
# другими пользователями — curl, например, под непривилегированным, — и
# такой ключ им не прочитать: соединение падает с «unable to set private
# key file».
#
# На смонтированном диске Linux это не проявляется: там права не
# хранятся, и всё выглядит доступным. Поймалось только на Windows.
#
# Здесь права снимаются осознанно: это одноразовые учебные ключи,
# заведомо не выходящие за пределы стенда, и они не коммитятся. В
# настоящем окружении так делать нельзя.
chmod 0644 ./*.key

rm -f ./*.csr ./*.ext ./*.cnf

echo "выпущено: опорная дата $REF, домен $DOMAIN"
ls -1 ./*.pem | sed 's|^\./|  |'
