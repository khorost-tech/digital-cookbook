// инициализация single-node RS (если ещё не)
try {
  rs.status();
} catch (e) {
  rs.initiate({ _id: 'rs0', members: [{ _id: 0, host: 'localhost:27017' }] });
  sleep(3000);
}

const db = db.getSiblingDB('waldemo');
db.t.drop();
for (let i = 0; i < 500; i++) db.t.insertOne({ i, v: Math.random().toString(36) });
db.t.updateMany({}, { $set: { touched: true } });

const oplog = db.getSiblingDB('local').oplog.rs;

print('=== счётчик записей oplog по op для waldemo.t (ожидаем 500 i + 500 u) ===');
oplog.aggregate([
  { $match: { ns: 'waldemo.t' } },
  { $group: { _id: '$op', count: { $sum: 1 } } },
]).forEach(printjson);

print('=== пример записи op: i (insert) ===');
printjson(oplog.findOne({ ns: 'waldemo.t', op: 'i' }));

print('=== пример записи op: u (update) ===');
printjson(oplog.findOne({ ns: 'waldemo.t', op: 'u' }));

// natural-order в oplog — это порядок применения: все 500 insert были применены
// раньше, чем updateMany прошёлся по коллекции и дописал 500 update следом.
// Поэтому «последние 5 по $natural» — это ХВОСТ из update-записей, а не срез
// вперемешку: увидеть insert среди «последних N» невозможно, пока N < 500.
print('=== последние 5 записей oplog по $natural (реально видим только op: u — см. комментарий выше) ===');
oplog.find({ ns: 'waldemo.t' }).sort({ $natural: -1 }).limit(5).forEach(e => printjson({ op: e.op, ns: e.ns, ts: e.ts }));

print('=== oplog — capped? размер ===');
printjson(db.getSiblingDB('local').runCommand({ collStats: 'oplog.rs' }).capped);
