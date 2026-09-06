#!/usr/bin/env bash
#
# backup-restore.sh — демонстрация backup/DR для Keycloak НЕ на словах, а end-to-end.
#
# Единственный источник ДОЛГОВЕЧНОГО состояния Keycloak — внешний PostgreSQL (realm,
# клиенты, пользователи, персистентные сессии; краткоживущий runtime-стейт — только в
# кэшах). Значит бэкап = логический дамп этой БД, а восстановление = залить дамп в
# чистый PostgreSQL. Плюс realm как код (realm/demo-realm.json) — декларативный слепок
# конфигурации, который импортируется на старте. Здесь показываем оба уровня.
#
# Сценарий:
#   1. Поднять стенд, дождаться готовности, снять базовый токен bob (200).
#   2. Завести «канарейку» — пользователя dr-canary-<ts>, которого НЕТ в realm-экспорте.
#      Он существует ТОЛЬКО в БД → отличает восстановление из дампа от повторного
#      импорта realm из JSON.
#   3. pg_dump БД keycloak (дамп включает канарейку) → файл, показать размер.
#   4. АВАРИЯ: docker compose down -v — контейнеры и том pgdata уничтожаются.
#   5. Поднять ЧИСТЫЙ PostgreSQL (свежий том, пустая БД keycloak).
#   6. Восстановить дамп в пустую БД (psql).
#   7. Поднять Keycloak. На восстановленной БД realm demo уже есть → --import-realm его
#      пропускает; канарейка сохраняется.
#   8. Ассерты: realm demo на месте; канарейка на месте (доказывает restore, а не
#      реимпорт); токен bob снова выдаётся (HTTP 200).
#
# Требования: docker (+compose), curl. Все креды — demo-only. Стенд остаётся поднятым
# (teardown: bash scripts/down.sh).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

REALM="${REALM:-demo}"
BOB_PWD="${BOB_PWD:-bob-demo-2026}"
CANARY="dr-canary-$(date +%s)"
DUMP="$(mktemp -t kc-dump.XXXXXX.sql)"
RESTORE_LOG="$(mktemp -t kc-restore.XXXXXX.log)"
TOKEN_URL="http://localhost:8080/realms/$REALM/protocol/openid-connect/token"
CERTS_URL="http://localhost:8080/realms/$REALM/protocol/openid-connect/certs"
USERINFO_URL="http://localhost:8080/realms/$REALM/protocol/openid-connect/userinfo"
ORIG_LIFESPAN=""

# python нужен для декодирования kid из JWT-заголовка и разбора JWKS (проверка, что
# подписные ключи восстановились из БД, а не были перегенерированы).
PY="$(command -v python3 || command -v python || true)"
[ -n "$PY" ] || { echo "[dr] нужен python3 или python в PATH" >&2; exit 1; }

COMPOSE=(docker compose)
log() { echo "[dr] $*" >&2; }
cid() { "${COMPOSE[@]}" ps -q "$1"; }
kcadm() { MSYS_NO_PATHCONV=1 docker exec -i "$(cid keycloak)" /opt/keycloak/bin/kcadm.sh "$@"; }

bob_access_token() { # печатает access_token (JWT) bob или пусто; scope=openid нужен,
  # чтобы токен принимал userinfo-эндпоинт (без openid он вернёт 403 insufficient_scope).
  curl -s --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1 \
    -X POST "$TOKEN_URL" \
    -d grant_type=password -d client_id=cli -d scope=openid -d username=bob -d "password=$BOB_PWD" |
    "$PY" -c 'import sys,json; print(json.load(sys.stdin).get("access_token",""))' 2>/dev/null
}

jwt_kid() { # stdin: JWT → печатает kid из заголовка (первый сегмент, base64url)
  "$PY" -c 'import sys,json,base64
seg=sys.stdin.read().strip().split(".")[0]
seg+="="*(-len(seg)%4)
print(json.loads(base64.urlsafe_b64decode(seg)).get("kid","")) if seg else print("")' 2>/dev/null
}

jwks_kids() { # печатает kid'ы из JWKS realm через пробел (устойчиво: retry + не рушит set -e)
  curl -s --retry 8 --retry-all-errors --retry-delay 1 "$CERTS_URL" |
    "$PY" -c 'import sys,json
try:
    d=json.load(sys.stdin); print(" ".join(k.get("kid","") for k in d.get("keys",[])))
except Exception:
    print("")' 2>/dev/null || true
}

jwk_modulus() { # $1=kid → печатает поле n (RSA-модуль) этого ключа из JWKS (материал ключа)
  curl -s --retry 8 --retry-all-errors --retry-delay 1 "$CERTS_URL" |
    KID="$1" "$PY" -c 'import sys,json,os
kid=os.environ["KID"]
try:
    d=json.load(sys.stdin)
    print(next((k.get("n","") for k in d.get("keys",[]) if k.get("kid")==kid and k.get("kty")=="RSA"), ""))
except Exception:
    print("")' 2>/dev/null || true
}

wait_healthy() { # $1 = service
  local id
  for _ in $(seq 1 60); do
    id="$(cid "$1")"
    if [ -n "$id" ] && [ "$(docker inspect --format '{{.State.Health.Status}}' "$id" 2>/dev/null)" = healthy ]; then
      return 0
    fi
    sleep 2
  done
  log "service $1 не стал healthy"; return 1
}

pg_ready() { # ждём готовности postgres (pg_isready), даже без healthcheck-статуса
  for _ in $(seq 1 60); do
    if docker exec -i "$(cid postgres)" pg_isready -U keycloak -d keycloak >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  log "postgres не готов"; return 1
}

bob_token_code() { # печатает HTTP-код token-эндпоинта для bob
  curl -s -o /dev/null -w '%{http_code}' \
    --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1 \
    -X POST "$TOKEN_URL" \
    -d grant_type=password -d client_id=cli -d username=bob -d "password=$BOB_PWD"
}

cleanup() {
  rm -f "$DUMP" "$RESTORE_LOG"
  # Восстановить исходный accessTokenLifespan (мы его расширяли, чтобы старый токен
  # пережил DR-прогон). KC на выходе поднят; если нет — тихо пропускаем.
  local id; id="$(cid keycloak 2>/dev/null || true)"
  if [ -n "$id" ] && [ -n "$ORIG_LIFESPAN" ]; then
    kcadm update "realms/$REALM" -s "accessTokenLifespan=$ORIG_LIFESPAN" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# --- 1. Поднять стенд ------------------------------------------------------------
log "up стенда…"
"${COMPOSE[@]}" up -d --build >/dev/null 2>&1
wait_healthy postgres
wait_healthy keycloak
kcadm config credentials --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1

BASE_CODE="$(bob_token_code)"
log "базовый токен bob ДО аварии: HTTP $BASE_CODE"
[ "$BASE_CODE" = 200 ] || { log "базовый токен не выдан — прерываю"; exit 1; }

# Зафиксировать подписной kid ДО бэкапа: после restore проверим, что тот же ключ есть
# в JWKS. Это доказывает, что из дампа восстановились именно ПРЕЖНИЕ ключи подписи, а не
# Keycloak сгенерировал новые (иначе выданный после restore токен ничего бы не доказывал).
# Расширяем accessTokenLifespan, чтобы токен, выданный ДО аварии, пережил весь DR-прогон:
# после restore предъявим ИМЕННО ЕГО (доказательство «прежние токены остаются валидны»),
# а не выпустим новый. Исходное значение вернём trap'ом на выходе.
ORIG_LIFESPAN="$(kcadm get "realms/$REALM" --fields accessTokenLifespan 2>/dev/null | "$PY" -c 'import sys,json;print(json.load(sys.stdin).get("accessTokenLifespan",300))' 2>/dev/null || echo 300)"
kcadm update "realms/$REALM" -s accessTokenLifespan=1800 >/dev/null 2>&1 || true

# Токен bob, выданный ДО бэкапа (полный JWT). Из него берём kid; сам токен предъявим после restore.
PRE_TOKEN="$(bob_access_token)"
PRE_KID="$(printf '%s' "$PRE_TOKEN" | jwt_kid)"
log "подписной kid ДО бэкапа (из access-token bob): ${PRE_KID:-<пусто>}"
{ [ -n "$PRE_TOKEN" ] && [ -n "$PRE_KID" ]; } || { log "не удалось получить токен/kid — прерываю"; exit 1; }
# Снимаем и сам МАТЕРИАЛ ключа (RSA-модуль n) — совпадение kid доказывает «тот же
# идентификатор», а совпадение n доказывает, что это байт-в-байт ТОТ ЖЕ ключ.
PRE_JWK_N="$(jwk_modulus "$PRE_KID")"
log "модуль (n) подписного ключа ДО бэкапа снят (${#PRE_JWK_N} симв.)"
[ -n "$PRE_JWK_N" ] || { log "не удалось снять модуль ключа $PRE_KID из JWKS — прерываю"; exit 1; }

# --- 2. Канарейка (только в БД, нет в realm-экспорте) ----------------------------
log "создаю канарейку username=$CANARY (её нет в realm/demo-realm.json)…"
kcadm create users -r "$REALM" -s "username=$CANARY" -s enabled=true >/dev/null 2>&1
kcadm get users -r "$REALM" -q "username=$CANARY" --fields username >&2

# --- 3. Бэкап: pg_dump -----------------------------------------------------------
log "pg_dump БД keycloak → $DUMP …"
docker exec -i "$(cid postgres)" pg_dump -U keycloak --no-owner --no-privileges keycloak > "$DUMP"
DUMP_SIZE="$(wc -c < "$DUMP" | tr -d ' ')"
log "дамп снят: ${DUMP_SIZE} байт ($(( DUMP_SIZE / 1024 )) KiB)"
[ "$DUMP_SIZE" -gt 10000 ] || { log "дамп подозрительно мал — прерываю"; exit 1; }

# --- 4. АВАРИЯ: уничтожаем том с данными -----------------------------------------
log "АВАРИЯ: docker compose down -v (том pgdata уничтожается)…"
"${COMPOSE[@]}" down -v >/dev/null 2>&1

# --- 5. Чистый PostgreSQL --------------------------------------------------------
log "поднимаю ЧИСТЫЙ postgres (пустая БД keycloak)…"
"${COMPOSE[@]}" up -d postgres >/dev/null 2>&1
pg_ready
# Убедимся, что БД пуста (канарейки/realm ещё нет): таблиц Keycloak быть не должно.
PRE_TABLES="$(docker exec -i "$(cid postgres)" psql -U keycloak -d keycloak -tAc \
  "select count(*) from information_schema.tables where table_schema='public'" 2>/dev/null | tr -d ' ')"
log "таблиц в public ДО restore: ${PRE_TABLES:-?} (ожидаем 0 — БД чистая)"

# --- 6. Восстановление из дампа --------------------------------------------------
# ON_ERROR_STOP=1 и БЕЗ `|| true`: частично удавшийся restore НЕ должен считаться
# успехом. Любая ошибка psql прерывает DR-сценарий с ненулевым кодом — так и должна
# вести себя проверенная процедура восстановления.
log "restore: psql < дамп (ON_ERROR_STOP=1 — любая ошибка прерывает DR) …"
if ! docker exec -i "$(cid postgres)" psql -U keycloak -d keycloak -v ON_ERROR_STOP=1 -q < "$DUMP" >"$RESTORE_LOG" 2>&1; then
  log "ASSERT FAIL: restore завершился с ошибкой psql (ON_ERROR_STOP=1). Последние строки:"
  tail -n 20 "$RESTORE_LOG" >&2
  exit 1
fi
log "restore прошёл без ошибок psql"
POST_TABLES="$(docker exec -i "$(cid postgres)" psql -U keycloak -d keycloak -tAc \
  "select count(*) from information_schema.tables where table_schema='public'" 2>/dev/null | tr -d ' ')"
log "таблиц в public ПОСЛЕ restore: ${POST_TABLES:-?}"

# --- 7. Поднять Keycloak на восстановленной БД -----------------------------------
log "поднимаю Keycloak на восстановленной БД…"
"${COMPOSE[@]}" up -d keycloak >/dev/null 2>&1
wait_healthy keycloak
kcadm config credentials --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1

# --- 8. Ассерты ------------------------------------------------------------------
log "=== ПРОВЕРКИ ПОСЛЕ ВОССТАНОВЛЕНИЯ ==="

REALM_JSON="$(kcadm get "realms/$REALM" --fields id,realm,enabled 2>/dev/null || true)"
printf '%s\n' "$REALM_JSON" | sed 's/^/   /' >&2
echo "$REALM_JSON" | grep -q "\"realm\" : \"$REALM\"" \
  || { log "ASSERT FAIL: realm $REALM не найден после restore"; exit 1; }
log "ASSERT OK: realm $REALM на месте"

CANARY_JSON="$(kcadm get users -r "$REALM" -q "username=$CANARY" --fields username 2>/dev/null || true)"
echo "$CANARY_JSON" | grep -q "\"$CANARY\"" \
  || { log "ASSERT FAIL: канарейка $CANARY отсутствует — данные пришли НЕ из бэкапа (реимпорт realm)"; exit 1; }
log "ASSERT OK: канарейка $CANARY восстановлена из дампа (значит restore сработал, а не реимпорт)"

# Проверка подписных ключей: тот же kid, что был ДО бэкапа, должен присутствовать в
# JWKS после restore. Если бы Keycloak перегенерировал ключи, kid бы отличался — и
# токены, выданные до аварии, перестали бы валидироваться. Совпадение kid доказывает,
# что realm_keys восстановлены из дампа.
POST_KIDS="$(jwks_kids || true)"
log "kid'ы в JWKS ПОСЛЕ restore: ${POST_KIDS:-<пусто>}"
case " $POST_KIDS " in
  *" $PRE_KID "*) log "ASSERT OK: подписной ключ kid=$PRE_KID сохранён после restore — ключи подписи восстановлены из БД, а не перегенерированы" ;;
  *) log "ASSERT FAIL: kid=$PRE_KID отсутствует в JWKS после restore — ключи подписи НЕ восстановились (перегенерированы)"; exit 1 ;;
esac

# Сильнее, чем совпадение kid: сравниваем сам материал ключа (RSA-модуль n) байт-в-байт.
# Совпадение kid — это совпадение идентификатора; совпадение n — доказательство, что за
# идентификатором стоит ТОТ ЖЕ ключ, а не новый под тем же kid.
POST_JWK_N="$(jwk_modulus "$PRE_KID")"
if [ -n "$POST_JWK_N" ] && [ "$POST_JWK_N" = "$PRE_JWK_N" ]; then
  log "ASSERT OK: материал ключа (RSA-модуль n) kid=$PRE_KID совпал байт-в-байт до и после restore — это тот же самый ключ"
else
  log "ASSERT FAIL: модуль ключа kid=$PRE_KID НЕ совпал до/после restore (до:${#PRE_JWK_N} после:${#POST_JWK_N} симв.) — ключ подменён/перегенерирован"; exit 1
fi

# Предъявляем ИМЕННО тот access-token, что был выдан ДО аварии, на userinfo восстановленного
# KC. Он должен приниматься: подпись валидна на восстановленных ключах, а сессия персистентна
# и восстановлена из дампа. Это прямая проверка «неистёкшие токены остаются валидны», а не
# выпуск нового токена.
PRE_TOKEN_CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PRE_TOKEN" "$USERINFO_URL")"
log "СТАРЫЙ токен (выдан ДО бэкапа) на userinfo ПОСЛЕ restore: HTTP $PRE_TOKEN_CODE"
[ "$PRE_TOKEN_CODE" = 200 ] || { log "ASSERT FAIL: токен, выданный до аварии, НЕ принят после restore (HTTP $PRE_TOKEN_CODE)"; exit 1; }
log "ASSERT OK: неистёкший токен, выданный ДО аварии, принят восстановленным KC (userinfo 200)"

AFTER_CODE="$(bob_token_code)"
log "токен bob ПОСЛЕ восстановления: HTTP $AFTER_CODE"
[ "$AFTER_CODE" = 200 ] || { log "ASSERT FAIL: токен bob не выдан после restore (HTTP $AFTER_CODE)"; exit 1; }
log "ASSERT OK: токен bob выдаётся (HTTP 200)"

echo "" >&2
log "DR-демо пройдено: pg_dump(${DUMP_SIZE}B) → down -v → restore → realm demo + канарейка + ключ $PRE_KID (kid И материал n совпали) + СТАРЫЙ токен принят (userinfo 200) + новый токен bob 200."
log "Комплементарный уровень (config as code): realm/demo-realm.json импортируется на старте на пустой БД."
log "Стенд оставлен поднятым (keycloak+postgres). Полный up: bash scripts/up.sh · teardown: bash scripts/down.sh"
