box.cfg{}
box.schema.space.create('events', {if_not_exists=true})
-- Примечание (адаптация под Tarantool 3): RTREE-индекс требует ОДНО поле типа
-- 'array' (координатная пара [lat, lon]), а не два отдельных 'number'-поля.
-- Иначе: "Field 5 (lat) has type 'number' in space format, but type 'array'
-- in index definition". Поэтому вместо lat/lon — единое поле coord={lat, lon}.
box.space.events:format({
  {name='id', type='unsigned'}, {name='user_id', type='unsigned'},
  {name='status', type='string'}, {name='flags', type='unsigned'},
  {name='coord', type='array'}})
-- TREE (первичный, упорядоченный диапазонный)
box.space.events:create_index('primary', {type='TREE', parts={'id'}, if_not_exists=true})
-- HASH (только равенство, O(1))
box.space.events:create_index('by_user', {type='HASH', parts={'user_id'}, unique=true, if_not_exists=true})
-- BITSET (поиск по битовым маскам/наборам)
box.space.events:create_index('by_flags', {type='BITSET', parts={'flags'}, unique=false, if_not_exists=true})
-- RTREE (пространственный, поле 5 = coord, type='array')
box.space.events:create_index('by_geo', {type='RTREE', parts={{5, 'array'}}, unique=false, if_not_exists=true})

-- Примечание (адаптация): в исходном примере статьи user_id = i % 10000 при 50000 строках даёт
-- 5-кратные дубликаты, что ломает unique=true на HASH-индексе ("Duplicate key
-- exists in unique index by_user"). Модуль увеличен до 100000 (> диапазона i),
-- чтобы user_id оставался уникальным и демонстрация HASH-равенства была честной.
for i=1,50000 do
  box.space.events:replace{i, i%100000, ({'new','paid','shipped'})[math.random(3)], math.random(0,255), {math.random()*180-90, math.random()*360-180}}
end

-- Примечание (адаптация): print() пишет в лог СЕРВЕРА (docker compose logs),
-- а не в stdout сессии `tt connect` -- run.sh ничего бы не увидел и grep-
-- проверки из исходного примера (`grep vinyl /tmp/tt.txt` и т.п.) не сработали бы.
-- Поэтому вывод собирается в таблицу и возвращается через `return` — так
-- `tt connect` печатает результат как YAML в СВОЙ stdout.
local out = {}
table.insert(out, '=== HASH: равенство по user_id ===')
table.insert(out, require('json').encode(box.space.events.index.by_user:select{42}))
table.insert(out, '=== TREE primary диапазон id=[1..3] ===')
table.insert(out, tostring(#box.space.events.index.primary:select({1}, {iterator='GE', limit=3})))
table.insert(out, '=== BITSET по flags ===')
table.insert(out, tostring(#box.space.events.index.by_flags:select(1, {iterator='BITS_ANY_SET'})))
table.insert(out, '=== индексы спейса ===')
for name,_ in pairs(box.space.events.index) do if type(name)=='string' then table.insert(out, 'idx: '..name) end end

-- vinyl (LSM): отдельный спейс
box.schema.space.create('events_lsm', {engine='vinyl', if_not_exists=true})
box.space.events_lsm:create_index('primary', {parts={{1,'unsigned'}}, if_not_exists=true})
table.insert(out, '=== vinyl-спейс создан, engine='..box.space.events_lsm.engine)

-- Примечание (адаптация): os.exit(0) из исходного примера убран. Скрипт выполняется не в
-- отдельном одноразовом процессе tarantool, а через `tt connect` в консоли
-- УЖЕ запущенного инстанса (см. run.sh) — os.exit(0) там завершает сам
-- инстанс/контейнер, а не только сессию.
return out
