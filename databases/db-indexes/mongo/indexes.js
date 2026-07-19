const db = db.getSiblingDB('idxdemo');
db.events.drop();
// Детерминированный сид: mulberry32 вместо Math.random(), чтобы датасет и вытекающие
// из него числа executionStats были байт-в-байт воспроизводимы между прогонами.
// (Простой LCG с маской 0x7fffffff даёт вырожденное распределение младших битов —
// на 200k документов получалось всего ~15к различных userId вместо ожидаемых ~86к;
// mulberry32 — компактный PRNG с полным перемешиванием битов, без этой проблемы.)
let _s = 42;
function rnd() {
  _s |= 0; _s = (_s + 0x6D2B79F5) | 0;
  let t = Math.imul(_s ^ (_s >>> 15), 1 | _s);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
}
// 200k документов
const bulk = [];
for (let i = 0; i < 200000; i++) {
  bulk.push({ userId: (rnd()*100000)|0,
    status: ['new','paid','shipped','cancelled','refunded'][(rnd()*5)|0],
    amount: Math.round(rnd()*1000*100)/100,
    tags: [['a','b','c','d'][(rnd()*4)|0], ['x','y'][(rnd()*2)|0]],
    createdAt: new Date(Date.now() - (rnd()*365)*86400000) });
  if (bulk.length === 10000) { db.events.insertMany(bulk); bulk.length = 0; }
}
if (bulk.length) db.events.insertMany(bulk);

print('=== COLLSCAN без индекса ===');
printjson(db.events.find({userId: 555}).explain('executionStats').executionStats);

print('=== IXSCAN после single-field ===');
db.events.createIndex({userId: 1});
printjson(db.events.find({userId: 555}).explain('executionStats').executionStats);

print('=== ESR: compound {status:1(eq), createdAt:1(sort), amount:1(range)} ===');
db.events.createIndex({status: 1, createdAt: 1, amount: 1});
printjson(db.events.find({status:'paid', amount:{$lt:100}}).sort({createdAt:1}).explain('executionStats').executionStats);

print('=== multikey: индекс по массиву tags ===');
db.events.createIndex({tags: 1});
printjson(db.events.find({tags:'a'}).explain('executionStats').executionStats);

print('=== partial: только paid ===');
// Явное имя: {userId:1} уже занят обычным индексом из блока IXSCAN выше,
// partialFilterExpression делает это другим индексом — mongosh не разрешает
// автогенерируемое имя "userId_1" дважды (IndexKeySpecsConflict).
db.events.createIndex({userId:1}, {name: 'userId_1_partial_paid', partialFilterExpression:{status:'paid'}});
printjson(db.events.find({userId:555, status:'paid'}).explain('executionStats').executionStats);

print('=== TTL: удаление по createdAt ===');
db.events.createIndex({createdAt:1}, {expireAfterSeconds: 60*60*24*400});

print('=== список индексов ===');
printjson(db.events.getIndexes());
