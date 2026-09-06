#!/usr/bin/env bash
#
# broker-demo.sh — демонстрация identity brokering: Keycloak как OIDC-брокер к
# ВНЕШНЕМУ провайдеру (mock-oauth2-server, роль Яндекс/VK/корпоративного IdP).
#
# Сценарий (authorization code flow, полностью неинтерактивно через curl):
#   1. Клиент `frontend` инициирует вход в Keycloak с kc_idp_hint=mock →
#      Keycloak сразу редиректит на authorization endpoint внешнего IdP.
#   2. mock-oauth2-server (interactiveLogin=false) без формы логина редиректит
#      обратно на broker-endpoint Keycloak с authorization code.
#   3. Keycloak СЕРВЕРНО обменивает code на токен внешнего IdP (mock-oidc:8083),
#      валидирует подпись по JWKS, прогоняет first-broker-login, ЗАВОДИТ/СВЯЗЫВАЕТ
#      локального пользователя (claim'ы → username/email/имя, hardcoded role `user`)
#      и редиректит на redirect_uri клиента с УЖЕ СВОИМ authorization code.
#   4. Обмениваем код Keycloak на access_token Keycloak (PKCE S256).
#
# В финале через kcadm показываем, что brokered-пользователь появился в realm и
# связан с федеративной идентичностью `mock`.
#
# Нюанс hostname (тот же класс, что с KC_HOSTNAME в Task 3): realm выдаёт токены с
# iss=http://keycloak:8080/realms/demo, поэтому все browser-facing редиректы Keycloak
# идут на хост `keycloak:8080`. С машины-хоста этот hostname не резолвится (и может
# перехватываться корпоративным HTTP-прокси), поэтому curl направляется на
# опубликованный порт через `--connect-to keycloak:8080:127.0.0.1:8080` и `--noproxy
# keycloak`. Внешний IdP (authorization endpoint) при этом доступен и с хоста —
# http://localhost:8083/default/authorize. Для интерактивного входа человеком в
# браузере достаточно добавить `127.0.0.1 keycloak` в hosts (в проде KC_HOSTNAME =
# реальный домен, и хост браузера совпадает с issuer — костылей не нужно).
#
# Требования: docker (compose-стенд поднят), curl, openssl, python3 (или python — для
# разбора JSON kcadm в ассертах). Все креды — demo-only.
set -euo pipefail

# Единый интерпретатор Python (python3 предпочтительно, откат на python).
PY="$(command -v python3 || command -v python || true)"
[ -n "$PY" ] || { echo "нужен python3 или python в PATH" >&2; exit 1; }

KC_HOST="${KC_HOST:-keycloak:8080}"                 # hostname из issuer/KC_HOSTNAME
KC_BASE="http://$KC_HOST"
MOCK_BASE="${MOCK_BASE:-http://localhost:8083}"     # внешний IdP с точки зрения хоста
REALM="${REALM:-demo}"
IDP_ALIAS="${IDP_ALIAS:-mock}"
CLIENT_ID="${CLIENT_ID:-frontend}"
REDIRECT_URI="${REDIRECT_URI:-http://localhost:3000/cb}"
KC_CONTAINER="${KC_CONTAINER:-keycloak-keycloak-1}"
EXPECTED_USER="${EXPECTED_USER:-ext-yandex-demo}"

# Направляем host `keycloak:8080` на опубликованный порт 127.0.0.1:8080 и обходим
# HTTP-прокси для этого имени. Так browser-facing редиректы Keycloak доступны с хоста.
# --retry: опубликованный порт Docker Desktop на Windows изредка «залипает» на первом
# коннекте — ретраи делают прогон детерминированным.
# ВАЖНО: curl --noproxy ПЕРЕЗАПИСЫВАет список NO_PROXY целиком, поэтому здесь должны
# быть перечислены ВСЕ хосты, к которым ходим напрямую (keycloak, localhost, 127.0.0.1),
# иначе localhost:8083 (внешний IdP) уйдёт в HTTP-прокси и вернёт 503.
CURL=(curl -s --noproxy "keycloak,localhost,127.0.0.1"
      --connect-to "keycloak:8080:127.0.0.1:8080"
      -m 15 --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1)

WORK="$(mktemp -d)"
CJ="$WORK/cookies.txt"
HDR="$WORK/headers.txt"
trap 'rm -rf "$WORK"' EXIT

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
rand()   { openssl rand -hex 24; }

# kcadm внутри контейнера (по абсолютным путям — MSYS_NO_PATHCONV=1, чтобы Git Bash
# не преобразовывал /opt/... в windows-путь).
kcadm() { MSYS_NO_PATHCONV=1 docker exec -i "$KC_CONTAINER" /opt/keycloak/bin/kcadm.sh "$@"; }

location_of() { grep -i '^location:' "$HDR" | tail -1 | sed -E 's/^[Ll]ocation:[[:space:]]*//; s/\r$//'; }
short_of()    { printf '%s' "$1" | sed -E 's#https?://([^/]+)(/[^?]*).*#\1\2#'; }

echo "== 1. Ждём готовности mock-OIDC discovery =="
for i in $(seq 1 30); do
  if curl -s -f "$MOCK_BASE/default/.well-known/openid-configuration" >/dev/null; then
    echo "   mock-OIDC готов: $MOCK_BASE/default"
    break
  fi
  sleep 1
  [ "$i" = 30 ] && { echo "mock-OIDC не поднялся" >&2; exit 1; }
done

echo "== 2. PKCE + инициируем brokered-логин через IdP '$IDP_ALIAS' =="
VERIFIER="$(rand)$(rand)"
CHALLENGE="$(printf '%s' "$VERIFIER" | openssl dgst -sha256 -binary | b64url)"
STATE="$(rand)"
NONCE="$(rand)"

AUTH_URL="$KC_BASE/realms/$REALM/protocol/openid-connect/auth"
AUTH_URL+="?client_id=$CLIENT_ID&response_type=code&scope=openid"
AUTH_URL+="&redirect_uri=$REDIRECT_URI&state=$STATE&nonce=$NONCE"
AUTH_URL+="&code_challenge=$CHALLENGE&code_challenge_method=S256"
AUTH_URL+="&kc_idp_hint=$IDP_ALIAS"

# Проходим цепочку 302/303 вручную, неся cookie-jar Keycloak, пока не упрёмся в
# redirect_uri клиента (localhost:3000) — туда Keycloak кладёт СВОЙ код.
url="$AUTH_URL"
KC_CODE=""
RET_STATE=""
for hop in $(seq 1 12); do
  "${CURL[@]}" -o /dev/null -D "$HDR" -c "$CJ" -b "$CJ" "$url"
  status="$(head -1 "$HDR" | tr -d '\r')"
  loc="$(location_of)"
  echo "   hop $hop: $status  ($(short_of "$url"))"
  case "$loc" in
    "$REDIRECT_URI"*)
      KC_CODE="$(printf '%s' "$loc" | sed -nE 's/.*[?&]code=([^&]+).*/\1/p')"
      RET_STATE="$(printf '%s' "$loc" | sed -nE 's/.*[?&]state=([^&]+).*/\1/p')"
      echo "   финальный redirect на клиента — authorization code Keycloak получен"
      break
      ;;
    "")
      echo "   нет Location на hop $hop; тело ответа:" >&2
      "${CURL[@]}" -s -b "$CJ" "$url" | tr -d '\r' | grep -iE 'error|message' | head -3 >&2 || true
      break
      ;;
  esac
  url="$loc"
done

[ -n "$KC_CODE" ] || { echo "не удалось получить authorization code Keycloak" >&2; exit 1; }

# CSRF-защита: state в callback ДОЛЖЕН совпадать с отправленным. Несовпадение/отсутствие —
# признак подделки ответа (в реальном клиенте это обязательная проверка).
if [ "$RET_STATE" != "$STATE" ]; then
  echo "ASSERT FAIL: state в callback ('$RET_STATE') != отправленному ('$STATE') — возможный CSRF" >&2
  exit 1
fi
echo "   ASSERT OK: state в callback совпал с отправленным (CSRF-проверка): ${STATE:0:12}…"

echo "== 3. Обмен кода Keycloak на access_token (PKCE) =="
TOKENS="$("${CURL[@]}" -X POST "$KC_BASE/realms/$REALM/protocol/openid-connect/token" \
  -d grant_type=authorization_code \
  -d "client_id=$CLIENT_ID" \
  -d "code=$KC_CODE" \
  -d "redirect_uri=$REDIRECT_URI" \
  -d "code_verifier=$VERIFIER")"

ACCESS="$(printf '%s' "$TOKENS" | sed -E 's/.*"access_token":"([^"]+)".*/\1/')"
[ -n "$ACCESS" ] && [ "$ACCESS" != "$TOKENS" ] || { echo "токен не выдан: $TOKENS" >&2; exit 1; }

decode() { # payload JWT (base64url) → JSON
  local p; p="$(printf '%s' "$1" | cut -d. -f2)"
  local m=$(( ${#p} % 4 )); [ $m -eq 2 ] && p="${p}=="; [ $m -eq 3 ] && p="${p}="
  printf '%s' "$p" | tr '_-' '/+' | openssl base64 -d -A 2>/dev/null
}
echo "   Keycloak access_token payload (фрагмент):"
decode "$ACCESS" | tr ',' '\n' | grep -E '"iss"|"preferred_username"|"email"|"realm_access"|"sub"' | sed 's/^/     /'

echo "== 4. Ассерты kcadm: пользователь заведён, связан с IdP '$IDP_ALIAS', роль user =="
kcadm config credentials --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1
echo "   поиск пользователя username=$EXPECTED_USER:"
USER_JSON="$(kcadm get users -r "$REALM" -q "username=$EXPECTED_USER" \
  --fields id,username,email,firstName,lastName 2>/dev/null)"
printf '%s\n' "$USER_JSON" | sed 's/^/     /'

# (а) пользователь существует
USER_ID="$(printf '%s' "$USER_JSON" | "$PY" -c 'import sys,json; a=json.load(sys.stdin); print(a[0]["id"] if a else "")')"
[ -n "$USER_ID" ] || { echo "ASSERT FAIL: пользователь $EXPECTED_USER не заведён в realm" >&2; exit 1; }
echo "   ASSERT OK: brokered-пользователь заведён (id=$USER_ID)"

# (б) федеративная идентичность связана именно с IdP '$IDP_ALIAS'.
# Список users (brief representation) не возвращает federatedIdentities даже при
# --fields — берём их с отдельного sub-resource endpoint users/{id}/federated-identity.
FED_JSON="$(kcadm get "users/$USER_ID/federated-identity" -r "$REALM" 2>/dev/null)"
printf '%s\n' "$FED_JSON" | sed 's/^/     /'
printf '%s' "$FED_JSON" | IDP="$IDP_ALIAS" "$PY" -c '
import sys, json, os
fis = json.load(sys.stdin)
sys.exit(0 if any(f.get("identityProvider") == os.environ["IDP"] for f in fis) else 1)
' || { echo "ASSERT FAIL: у $EXPECTED_USER нет federated identity '$IDP_ALIAS'" >&2; exit 1; }
echo "   ASSERT OK: federated identity '$IDP_ALIAS' связана"

# (в) hardcoded-role-idp-mapper назначил realm-роль user
ROLES_JSON="$(kcadm get "users/$USER_ID/role-mappings/realm" -r "$REALM" 2>/dev/null)"
printf '%s' "$ROLES_JSON" | "$PY" -c 'import sys,json; rs=json.load(sys.stdin); sys.exit(0 if any(r.get("name")=="user" for r in rs) else 1)' \
  || { echo "ASSERT FAIL: realm-роль user не назначена $EXPECTED_USER" >&2; exit 1; }
echo "   ASSERT OK: realm-роль user назначена"

echo ""
echo "OK: brokered-логин через '$IDP_ALIAS' отработал — state проверен, пользователь заведён, federated identity связана, роль user назначена, токен Keycloak выдан."
