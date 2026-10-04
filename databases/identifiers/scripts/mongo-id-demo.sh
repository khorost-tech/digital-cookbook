#!/usr/bin/env bash
# Артефакт 3: _id в Mongo — ObjectId (монотонный, 12 байт) против UUID
# (случайный, 16 байт, BinData subtype 4). Вставляем одинаковое N в две
# коллекции, различие — только тип _id (одинаковый payload, insertMany
# ordered:false). Меряем размер _id-индекса структурно (один хост — не
# про производительность, а про то, что 16-байтный случайный ключ шире
# и не даёт последовательной вставки b-tree, как монотонный ObjectId).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
N=${N:-200000}
MONGO=(docker compose -f compose/compose.yml exec -T mongo mongosh --quiet "mongodb://localhost/ids")

# up.sh не ждёт готовности Mongo (исторически) — проверяем сами, иначе
# insertMany ниже может молча провалиться на ещё не поднятом сервере.
echo "[demo] проверка связи с MongoDB..." >&2
if ! "${MONGO[@]}" --eval "db.runCommand({ping:1})" >/dev/null 2>&1; then
  echo "DEMO FAILED: MongoDB недоступна (compose-mongo-1, localhost:27018 на хосте)" >&2
  exit 1
fi

"${MONGO[@]}" --eval "
  db.oid.drop(); db.uid.drop();
  const payload='x'.repeat(64); const bulkO=[], bulkU=[];
  for (let i=0;i<$N;i++){ bulkO.push({payload}); bulkU.push({_id:UUID(),payload}); }
  db.oid.insertMany(bulkO,{ordered:false});
  db.uid.insertMany(bulkU,{ordered:false});
  // Форс-чекпойнт WiredTiger: без него stats() читает indexSizes до
  // автоматического чекпойнта (интервал ~60с), и за ~5с вставки размер
  // может быть занижен/не сброшен на диск.
  db.adminCommand({fsync: 1});
  const so=db.oid.stats().indexSizes._id_, su=db.uid.stats().indexSizes._id_;
  print('_id index bytes: objectid='+so+' uuid='+su);
  if (su > so) { print('SIGNAL: UUID _id-индекс крупнее ObjectId'); quit(0); }
  else { print('DEMO FAILED: не крупнее (o='+so+' u='+su+')'); quit(1); }
"
