#!/usr/bin/env bash
# Артефакт 4b: MongoDB ranged sharding — горячий чанк от монотонного shard
# key. Реальный обратный трейдоф, которого нет у хеш-Scylla (см.
# scylla-token-demo.sh): Scylla всегда хеширует partition key (Murmur3),
# поэтому монотонный и случайный ключ выглядят на token-кольце одинаково
# широко. Mongo при RANGED shard key, наоборот, физически сохраняет порядок
# ключа: чанк — это диапазон значений. Монотонно растущий sk после
# pre-split на оба шарда всё равно льётся в ОДИН "правый" (последний,
# maxKey) чанк — весь новый трафик записи бьёт по одному шарду. HASHED
# shard key в Mongo ведёт себя как Scylla: хеширует sk, распределяет
# равномерно.
#
# Топология (одноузловые RS вместо честного PSS — учебный стенд, не прод):
#   mcfg   — configsvr RS "cfg"  :27019
#   msh1   — shardsvr  RS "s1"   :27021
#   msh2   — shardsvr  RS "s2"   :27022
#   mongos — роутер               :27020 (наружу)
# Тег образа — mongo:8.3.4, тот же, что у основного сервиса mongo в
# compose.yml (плейсхолдер 8.0 из брифа не используется).
#
# Балансировщик: ВЫКЛЮЧЕН (sh.stopBalancer()) перед вставкой. Цель —
# показать чистый эффект РОУТИНГА записи по shard key (chunk map),
# а не последующее перемешивание фоновым балансировщиком.
#
# Честность демонстрации (см. бриф): чтобы старт не был тривиально
# одношардовым, коллекция ranged ПРЕДВАРИТЕЛЬНО разбита на чанки на ОБОИХ
# шардах ДО вставки — sh.splitAt('ids.ranged', {sk:0}) создаёт границу
# [MinKey,0) / [0,MaxKey), затем moveChunk разводит их по s1/s2. Вставляемые
# документы имеют sk от 0 до N-1 (монотонно, как требует бриф) — весь этот
# диапазон лежит ВНУТРИ чанка [0,MaxKey), поэтому 100% новых записей уходят
# на шард, которому принадлежит этот чанк: чанки существуют на обоих
# шардах, но живая точка записи (правый край монотонного ключа) — одна.
# Коллекция hashed отличается ТОЛЬКО типом shard key (hashed вместо 1);
# те же значения sk, то же N — sh.shardCollection сама создаёт несколько
# начальных чанков, распределённых по обоим шардам через hash(sk).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
export PYTHONIOENCODING=utf-8
# Интерпретатор Python: в WSL/многих дистрибутивах есть только python3, на
# Windows Git Bash — только python. Берём python, затем fallback на python3
# (порядок неслучаен: на Windows `python3` часто указывает на нерабочую
# Store-заглушку, а рабочий бинарь называется python).
PY=$(command -v python || command -v python3 || true)
if [ -z "$PY" ]; then
  echo "DEMO FAILED: не найден интерпретатор Python (нужен python или python3)" >&2
  exit 1
fi
N=${N:-2000}
COMPOSE=(docker compose -f compose/compose.yml)

msh() { # msh <svc> <port> -- eval через mongosh на localhost:<port> внутри контейнера <svc>
  local svc="$1" port="$2"; shift 2
  "${COMPOSE[@]}" exec -T "$svc" mongosh --quiet "mongodb://localhost:${port}" "$@"
}
msh_ids() { # то же, но сразу в базе ids
  local svc="$1" port="$2"; shift 2
  "${COMPOSE[@]}" exec -T "$svc" mongosh --quiet "mongodb://localhost:${port}/ids" "$@"
}

# ВАЖНО: mongos сюда НЕ включаем. Роутер не принимает соединения, пока у
# config-server RS нет PRIMARY — на свежем стенде он не станет доступен за
# отведённое время и завалит проверку ДО того, как мы успеем инициировать
# RS. Порядок строгий: сначала mongod-узлы (mcfg/msh1/msh2, они отвечают на
# ping ещё до rs.initiate) → инициация RS (шаг 1) → только потом mongos
# (поднимаем и ждём его отдельно ниже).
echo "[demo] проверка связи с mongod-узлами mcfg/msh1/msh2..." >&2
for pair in "mcfg:27019" "msh1:27021" "msh2:27022"; do
  svc="${pair%%:*}"; port="${pair##*:}"
  tries=0
  until msh "$svc" "$port" --eval "db.runCommand({ping:1})" >/dev/null 2>&1; do
    tries=$((tries+1))
    if [ "$tries" -gt 30 ]; then
      echo "DEMO FAILED: $svc недоступен на порту $port (docker compose up -d mcfg msh1 msh2 не отработал)" >&2
      exit 1
    fi
    sleep 1
  done
done

# --- Шаг 1: инициализация трёх replica set'ов, ждём PRIMARY у каждого. ---
# rs.initiate() идемпотентно оборачиваем try/catch — "already initialized"
# означает, что скрипт уже запускался на этом же (не пересозданном) стеке.
rs_ensure() {
  local svc="$1" port="$2" rsid="$3" host="$4" extra="$5"
  echo "[demo] rs.initiate $rsid на $svc..." >&2
  msh "$svc" "$port" --eval "
    try {
      rs.initiate({_id:'$rsid'${extra},members:[{_id:0,host:'$host'}]});
    } catch (e) {
      if (!/already initialized/.test(e.message)) { throw e; }
    }
  " >/dev/null
  local tries=0
  until [ "$(msh "$svc" "$port" --eval 'rs.isMaster().ismaster' 2>/dev/null | tr -d '\r')" = "true" ]; do
    tries=$((tries+1))
    if [ "$tries" -gt 30 ]; then
      echo "DEMO FAILED: RS $rsid ($svc) не стал PRIMARY за отведённое время" >&2
      exit 1
    fi
    sleep 1
  done
}
rs_ensure mcfg 27019 cfg mcfg:27019 ",configsvr:true"
rs_ensure msh1 27021 s1 msh1:27021 ""
rs_ensure msh2 27022 s2 msh2:27022 ""

# Config-server RS теперь инициализирован и имеет PRIMARY — только сейчас
# поднимаем mongos. На свежем стенде роутер стартовал раньше config RS и
# висел в ретраях подключения; рестарт заставляет его подключиться к уже
# готовому config-серверу немедленно, не дожидаясь внутреннего backoff.
# up -d гарантирует, что контейнер существует и запущен (если он ранее
# отвалился в crash-loop); restart форсирует чистое переподключение. Оба
# под || true — авторитетным гейтом остаётся проверка ping ниже.
echo "[demo] (пере)запуск mongos после инициации config RS..." >&2
"${COMPOSE[@]}" up -d mongos >/dev/null 2>&1 || true
"${COMPOSE[@]}" restart mongos >/dev/null 2>&1 || true

echo "[demo] ждём готовности mongos..." >&2
tries=0
until msh mongos 27020 --eval "db.runCommand({ping:1})" >/dev/null 2>&1; do
  tries=$((tries+1))
  if [ "$tries" -gt 90 ]; then
    echo "DEMO FAILED: mongos не поднялся после инициации RS и рестарта" >&2
    exit 1
  fi
  sleep 1
done

# --- Шаг 2: добавить оба шарда, включить шардирование ids, выключить балансировщик. ---
msh mongos 27020 --eval "
  try { sh.addShard('s1/msh1:27021'); } catch (e) { if (!/already exists/.test(e.message) && !/already a shard/.test(String(e))) { throw e; } }
  try { sh.addShard('s2/msh2:27022'); } catch (e) { if (!/already exists/.test(e.message) && !/already a shard/.test(String(e))) { throw e; } }
  try { sh.enableSharding('ids'); } catch (e) { if (!/already enabled/.test(e.message)) { throw e; } }
  sh.stopBalancer();
" >/dev/null
echo "[demo] шарды добавлены, ids шардирована, балансировщик выключен." >&2

# --- Шаг 3: коллекция ranged — pre-split на оба шарда ДО вставки, затем N монотонных sk. ---
msh_ids mongos 27020 --eval "
  db.ranged.drop();
  sh.shardCollection('ids.ranged', {sk:1});
  sh.splitAt('ids.ranged', {sk:0});
  sh.moveChunk('ids.ranged', {sk:-1}, 's1');
  sh.moveChunk('ids.ranged', {sk:0}, 's2');
" >/dev/null
n_chunks_s1=$(msh_ids mongos 27020 --eval "db.getSiblingDB('config').chunks.countDocuments({shard:'s1'})" 2>/dev/null | tr -d '\r')
n_chunks_s2=$(msh_ids mongos 27020 --eval "db.getSiblingDB('config').chunks.countDocuments({shard:'s2'})" 2>/dev/null | tr -d '\r')
if [ "$n_chunks_s1" -lt 1 ] || [ "$n_chunks_s2" -lt 1 ]; then
  echo "DEMO FAILED: pre-split не дал чанков на обоих шардах (s1=$n_chunks_s1 s2=$n_chunks_s2) — старт был бы тривиально одношардовым" >&2
  exit 1
fi
echo "[demo] ranged: чанки до вставки — s1=$n_chunks_s1 s2=$n_chunks_s2 (оба шарда участвуют)." >&2

msh_ids mongos 27020 --eval "
  const payload='x'.repeat(64);
  const bulk=[];
  for (let i=0;i<$N;i++){ bulk.push({sk:i,payload}); }
  db.ranged.insertMany(bulk,{ordered:false});
" >/dev/null

# --- Шаг 4: коллекция hashed — тот же N, те же монотонные sk, только тип shard key другой. ---
msh_ids mongos 27020 --eval "
  db.hashed.drop();
  sh.shardCollection('ids.hashed', {sk:'hashed'});
  const payload='x'.repeat(64);
  const bulk=[];
  for (let i=0;i<$N;i++){ bulk.push({sk:i,payload}); }
  db.hashed.insertMany(bulk,{ordered:false});
" >/dev/null

# --- Шаг 5: распределение документов — считаем НАПРЯМУЮ на каждом шарде ---
# (обходим mongos: документ физически лежит на шарде, владеющем его
# чанком, поэтому countDocuments() прямо на msh1/msh2 даёт точные числа
# без парсинга текстового вывода getShardDistribution()).
r1=$(msh_ids msh1 27021 --eval "db.ranged.countDocuments({})" 2>/dev/null | tr -d '\r')
r2=$(msh_ids msh2 27022 --eval "db.ranged.countDocuments({})" 2>/dev/null | tr -d '\r')
h1=$(msh_ids msh1 27021 --eval "db.hashed.countDocuments({})" 2>/dev/null | tr -d '\r')
h2=$(msh_ids msh2 27022 --eval "db.hashed.countDocuments({})" 2>/dev/null | tr -d '\r')

echo "ranged: s1=$r1 s2=$r2   hashed: s1=$h1 s2=$h2" >&2

# --- Шаг 6: фальсификация. Различие коллекций — ТОЛЬКО тип shard key. ---
# SIGNAL только если ranged перекошен (max-шард >= 80% документов) И
# hashed равномерен (max-шард <= 65%). Иначе DEMO FAILED, rc=1.
"$PY" - "$N" "$r1" "$r2" "$h1" "$h2" <<'PY'
import sys
N, r1, r2, h1, h2 = (int(x) for x in sys.argv[1:6])
rt, ht = r1 + r2, h1 + h2
if rt != N or ht != N:
    print(f"DEMO FAILED: суммы по шардам не совпадают с N (ranged={rt} hashed={ht} N={N})")
    sys.exit(1)
r_pct = 100.0 * max(r1, r2) / rt
h_pct = 100.0 * max(h1, h2) / ht
R_THR, H_THR = 80.0, 65.0
print(f"ranged: s1={r1} ({100.0*r1/rt:.1f}%) s2={r2} ({100.0*r2/rt:.1f}%) -> max-шард {r_pct:.1f}%")
print(f"hashed: s1={h1} ({100.0*h1/ht:.1f}%) s2={h2} ({100.0*h2/ht:.1f}%) -> max-шард {h_pct:.1f}%")
if r_pct >= R_THR and h_pct <= H_THR:
    print(f"SIGNAL: ranged shard key на монотонном sk концентрирует {r_pct:.1f}% новых документов на одном шарде (>= {R_THR}%), "
          f"hashed shard key на тех же sk распределён равномерно ({h_pct:.1f}% <= {H_THR}%) — "
          f"реальный обратный трейдоф ranged-шардирования Mongo, которого нет у хеш-Scylla")
    sys.exit(0)
print(f"DEMO FAILED: пороги не выполнены (ranged {r_pct:.1f}% >= {R_THR}%? hashed {h_pct:.1f}% <= {H_THR}%?)")
sys.exit(1)
PY
