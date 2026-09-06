#!/usr/bin/env bash
#
# bench.sh — Task 7: сравнение двух режимов валидации access-токена в backend-go на
# одинаковой нагрузке (N воркеров × M запросов на /me):
#   AUTH_MODE=jwks       — локальная проверка подписи по JWKS (offline, кэш ключей);
#   AUTH_MODE=introspect — RFC 7662 introspection на Keycloak на КАЖДЫЙ запрос.
#
# Что меряется:
#   * latency backend-go (p50/p95/p99) и throughput — Go-нагрузчиком (bench/), который
#     запускается ВНУТРИ docker-сети стенда (сервис `bench`, профиль bench), а не с
#     хоста: NAT Docker Desktop на Windows искажает latency и даёт 000 на первых коннектах;
#   * сколько запросов реально дошло до Keycloak — по метрике Keycloak
#     http_server_requests_seconds_count с uri=".../token/introspect" на management-порту
#     9000 (--metrics-enabled). Снимаем счётчик ДО и ПОСЛЕ прогона, берём дельту.
#     Ожидание: jwks → дельта ≈ 0 (Keycloak не трогается на горячем пути), introspect →
#     дельта ≈ числу запросов (1 обращение к IdP на запрос).
#
# Переключение режима — пересозданием ТОЛЬКО backend-go: introspect включается оверлеем
# bench/compose.introspect.yml (AUTH_MODE=introspect + секрет), обратно в jwks — базовым
# compose. Каждый режим получает свежий токен непосредственно перед прогоном.
#
# Параметры (env): WORKERS (20), REQ (2000, на воркер), WARMUP (500) — как в статье 3
# (20×2000 = 40 000 замеряемых запросов). Стенд поднимается сам; teardown — НЕ делает
# (оставляет стенд поднятым; `down -v` — вручную/scripts/down.sh).
#
# Требования: docker (+compose), curl, python3 (или python — интерпретатор определяется
# автоматически). Все креды — demo-only.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

# Единый интерпретатор Python: предпочитаем python3 (Linux/CI), с откатом на python
# (Windows/Git Bash, где команды python3 может не быть). Так скрипт воспроизводимо
# запускается на обеих платформах, без падения `python: command not found`.
PY="$(command -v python3 || command -v python || true)"
[ -n "$PY" ] || { echo "[bench] нужен python3 или python в PATH" >&2; exit 1; }

WORKERS="${WORKERS:-20}"
REQ="${REQ:-2000}"
WARMUP="${WARMUP:-500}"
REALM="${REALM:-demo}"
# Пароль bob берём КАНОНИЧНЫЙ из realm-экспорта (realm/demo-realm.json), а НЕ переустанавливаем
# его: bench не должен оставлять realm в изменённом состоянии (иначе последующий
# scripts/backup-restore.sh, ожидающий этот же пароль, падал бы). Только чтение.
BOB_PWD="${BOB_PWD:-bob-demo-2026}"
TOKEN_LIFESPAN="${TOKEN_LIFESPAN:-900}"   # расширяем на время прогона, чтобы токен не истёк в introspect
ORIG_LIFESPAN=300                         # исходный accessTokenLifespan realm (realm/demo-realm.json); уточняется live ниже

COMPOSE=(docker compose)
INTROSPECT_OVERLAY=(-f docker-compose.yml -f bench/compose.introspect.yml)
METRICS_URL="http://localhost:9000/metrics"
TOKEN_URL="http://localhost:8080/realms/$REALM/protocol/openid-connect/token"

log() { echo "[bench] $*" >&2; }

cid() { "${COMPOSE[@]}" ps -q "$1"; }

kcadm() { # выполнить kcadm.sh внутри контейнера keycloak (MSYS_NO_PATHCONV — Git Bash не трогает /opt/...)
  MSYS_NO_PATHCONV=1 docker exec -i "$(cid keycloak)" /opt/keycloak/bin/kcadm.sh "$@"
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
  log "service $1 не стал healthy"
  return 1
}

get_token() {
  # --retry: опубликованный порт Docker Desktop на Windows изредка сбрасывает первый
  # коннект после пересоздания backend-go (пустой ответ 000) — ретраи делают выдачу
  # токена детерминированной, иначе json.load падает на пустом теле.
  curl -s -m 15 --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1 \
    -X POST "$TOKEN_URL" \
    -d grant_type=password -d client_id=cli -d username=bob -d "password=$BOB_PWD" |
    "$PY" -c 'import sys,json; print(json.load(sys.stdin)["access_token"])'
}

# ВСЕГДА восстанавливаем accessTokenLifespan в исходное значение при выходе (в т.ч. при
# ошибке/Ctrl-C): прогон временно расширяет его до $TOKEN_LIFESPAN, чтобы токен не истёк
# в introspect-режиме, но realm-экспорт рассчитан на дефолт 300 — стенд не должен
# оставаться с изменённым realm.
cleanup() {
  local id; id="$(cid keycloak 2>/dev/null || true)"
  [ -n "$id" ] || return 0
  kcadm update "realms/$REALM" -s "accessTokenLifespan=$ORIG_LIFESPAN" >/dev/null 2>&1 || true
  log "accessTokenLifespan восстановлен → $ORIG_LIFESPAN"
  # Гарантированно вернуть backend-go в дефолтный jwks-режим — на случай, если прогон
  # прервался в introspect-режиме (базовый compose = jwks). Иначе стенд остался бы
  # с backend в introspect после ошибки/Ctrl-C.
  "${COMPOSE[@]}" up -d --force-recreate --no-deps backend-go >/dev/null 2>&1 || true
  log "backend-go возвращён в jwks-режим"
}
trap cleanup EXIT

# Суммарный счётчик HTTP-запросов Keycloak к introspection-эндпоинту (все статусы). 0, если метрики ещё нет.
kc_introspect_count() {
  curl -s "$METRICS_URL" |
    awk '/^http_server_requests_seconds_count/ && /introspect/ {s+=$NF} END{printf "%d", s+0}'
}

# Прогон нагрузчика в docker-сети стенда. Токен — через env (не в argv). JSON результата → stdout, таблица → stderr.
# --no-deps ОБЯЗАТЕЛЕН: без него `docker compose run` (по базовому compose) видит, что запущенный
# backend-go отличается конфигом от базовой спеки (в introspect-режиме — из оверлея) и молча
# ПЕРЕСОЗДАЁТ его обратно в jwks перед запуском нагрузчика — introspect-замер выродился бы в jwks.
run_bench() { # $1 = label ; использует $TOK
  "${COMPOSE[@]}" --profile bench run --rm -T --no-deps -e "BENCH_TOKEN=$TOK" \
    bench -label "$1" -workers "$WORKERS" -requests "$REQ" -warmup "$WARMUP" -json-out -
}

# --- 0. Поднять стенд и собрать образ нагрузчика ----------------------------------
log "up стенда (build при необходимости)…"
# Ошибки сборки/подъёма НЕ прячем полностью: при провале печатаем stderr и выходим —
# иначе весь bench молча выродится в бессмысленный прогон на несобранном стенде.
if ! "${COMPOSE[@]}" up -d --build >/dev/null 2>/tmp/bench-up.err; then
  log "compose up/build упал:"; tail -n 30 /tmp/bench-up.err >&2; exit 1
fi
wait_healthy keycloak
wait_healthy backend-go
log "build образа нагрузчика (профиль bench)…"
"${COMPOSE[@]}" --profile bench build bench >/dev/null 2>&1

# --- 1. Параметры realm под прогон ------------------------------------------------
kcadm config credentials --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1 || true
# Пароль bob НЕ трогаем (используем каноничный из realm-экспорта) — realm не мутируем,
# кроме accessTokenLifespan, который восстанавливается trap'ом на выходе.
# Зафиксировать ИСХОДНЫЙ lifespan live (на случай, если realm уже отличается), затем расширить.
ORIG_LIFESPAN="$(kcadm get "realms/$REALM" --fields accessTokenLifespan 2>/dev/null \
  | "$PY" -c 'import sys,json; print(json.load(sys.stdin).get("accessTokenLifespan",300))' 2>/dev/null || echo 300)"
[ -n "$ORIG_LIFESPAN" ] || ORIG_LIFESPAN=300
log "исходный accessTokenLifespan = $ORIG_LIFESPAN (будет восстановлен на выходе)"
kcadm update "realms/$REALM" -s "accessTokenLifespan=$TOKEN_LIFESPAN" >/dev/null 2>&1 || true

# Секрет confidential-клиента backend для introspection (demo-only) из экспорта realm.
KC_BACKEND_SECRET="$("$PY" -c 'import json;print(next(c["secret"] for c in json.load(open("realm/demo-realm.json"))["clients"] if c["clientId"]=="backend"))')"
export KC_BACKEND_SECRET

# --- 2. Прогон JWKS ---------------------------------------------------------------
log "backend-go → AUTH_MODE=jwks (пересоздание)…"
"${COMPOSE[@]}" up -d --force-recreate --no-deps backend-go >/dev/null 2>&1
wait_healthy backend-go
TOK="$(get_token)"; [ -n "$TOK" ] || { log "не получил токен (jwks)"; exit 1; }
KC_BEFORE="$(kc_introspect_count)"
JWKS_JSON="$(run_bench jwks)"
KC_AFTER="$(kc_introspect_count)"
JWKS_KC=$((KC_AFTER - KC_BEFORE))
log "jwks: обращений к Keycloak (introspect) за прогон = $JWKS_KC"

# --- 3. Прогон INTROSPECT ---------------------------------------------------------
log "backend-go → AUTH_MODE=introspect (пересоздание с оверлеем)…"
"${COMPOSE[@]}" "${INTROSPECT_OVERLAY[@]}" up -d --force-recreate --no-deps backend-go >/dev/null 2>&1
wait_healthy backend-go
TOK="$(get_token)"; [ -n "$TOK" ] || { log "не получил токен (introspect)"; exit 1; }
KC_BEFORE="$(kc_introspect_count)"
INTRO_JSON="$(run_bench introspect)"
KC_AFTER="$(kc_introspect_count)"
INTRO_KC=$((KC_AFTER - KC_BEFORE))
log "introspect: обращений к Keycloak (introspect) за прогон = $INTRO_KC"

# СТРОГИЕ ассерты воспроизводимости (а не только печать чисел):
#   * jwks на горячем пути НЕ трогает IdP вообще → introspection-вызовов строго 0;
#   * introspect обращается к IdP на КАЖДЫЙ запрос → TOTAL замеряемых + WARMUP;
#   * оба режима без ошибок нагрузчика → errors == 0.
JWKS_ERR="$(printf '%s' "$JWKS_JSON"  | "$PY" -c 'import sys,json;print(json.load(sys.stdin).get("errors",-1))')"
INTRO_ERR="$(printf '%s' "$INTRO_JSON" | "$PY" -c 'import sys,json;print(json.load(sys.stdin).get("errors",-1))')"
TOTAL=$((WORKERS * REQ)); EXP_INTRO=$((TOTAL + WARMUP))
[ "$JWKS_KC" -eq 0 ]        || { log "ASSERT FAIL: jwks сделал $JWKS_KC introspection-вызовов (ожидали строго 0)"; exit 1; }
[ "$JWKS_ERR" = 0 ]        || { log "ASSERT FAIL: jwks errors=$JWKS_ERR (ожидали 0)"; exit 1; }
[ "$INTRO_ERR" = 0 ]       || { log "ASSERT FAIL: introspect errors=$INTRO_ERR (ожидали 0)"; exit 1; }
# Изолированный стенд: к /introspect обращается ТОЛЬКО нагрузчик, поэтому проверяем
# РОВНОЕ равенство TOTAL+WARMUP, а не диапазон.
[ "$INTRO_KC" -eq "$EXP_INTRO" ] \
  || { log "ASSERT FAIL: introspect сделал $INTRO_KC вызовов /introspect (ожидали РОВНО $EXP_INTRO = TOTAL+WARMUP)"; exit 1; }
log "ASSERT OK: jwks /introspect=0 errors=0; introspect /introspect=$INTRO_KC (=$EXP_INTRO=TOTAL+WARMUP) errors=0"

# Вернуть backend-go в дефолтный jwks-режим.
log "восстановление backend-go → jwks…"
"${COMPOSE[@]}" up -d --force-recreate --no-deps backend-go >/dev/null 2>&1
wait_healthy backend-go

# --- 4. Сравнительная таблица -----------------------------------------------------
JWKS_JSON="$JWKS_JSON" INTRO_JSON="$INTRO_JSON" JWKS_KC="$JWKS_KC" INTRO_KC="$INTRO_KC" \
WORKERS="$WORKERS" REQ="$REQ" PYTHONIOENCODING=utf-8 "$PY" <<'PY'
import json, os
j = json.loads(os.environ["JWKS_JSON"])
i = json.loads(os.environ["INTRO_JSON"])
jkc = int(os.environ["JWKS_KC"]); ikc = int(os.environ["INTRO_KC"])
total = int(os.environ["WORKERS"]) * int(os.environ["REQ"])

def row(name, r, kc):
    return "| {:<10} | {:>8.2f} | {:>8.2f} | {:>8.2f} | {:>12.0f} | {:>18} |".format(
        name, r["p50_ms"], r["p95_ms"], r["p99_ms"], r["throughput_rps"], f"{kc} / {r['total']}")

print()
print(f"=== Task 7: JWKS vs introspection ({os.environ['WORKERS']} воркеров × {os.environ['REQ']} = {total} запросов на /me) ===")
print("| mode       | p50 ms   | p95 ms   | p99 ms   | throughput   | introspect→KC/всего |")
print("|------------|----------|----------|----------|--------------|--------------------|")
print(row("jwks", j, jkc))
print(row("introspect", i, ikc))
print()
print(f"jwks:       ok={j['ok']} errors={j['errors']} status={j['status']}")
print(f"introspect: ok={i['ok']} errors={i['errors']} status={i['status']}")
if j["p50_ms"] > 0:
    print(f"introspect p50 / jwks p50 = ×{i['p50_ms']/j['p50_ms']:.1f}")
PY

log "готово. Стенд оставлен поднятым (teardown: docker compose down -v)."
