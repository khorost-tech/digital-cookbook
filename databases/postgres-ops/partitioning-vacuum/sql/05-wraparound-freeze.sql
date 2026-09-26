-- Transaction ID wraparound и freeze. XID 32-битный и идёт по кругу. Если строку не
-- «заморозить» (freeze) до того, как счётчик обойдёт ~2 млрд, старые данные вдруг
-- окажутся «из будущего» и пропадут. Чтобы этого не случилось, VACUUM замораживает
-- старые версии (проставляет им признак «видна всем всегда»), сбрасывая возраст.
-- Меряем возраст через age(relfrozenxid): это «сколько xid назад заморожена таблица».

DROP TABLE IF EXISTS freeze_demo;
CREATE TABLE freeze_demo (id bigserial PRIMARY KEY, val int);
INSERT INTO freeze_demo (val) SELECT g FROM generate_series(1, 1000) g;

\echo === исходный возраст таблицы (мал: только что создана) ===
SELECT age(relfrozenxid) AS xid_age
FROM pg_class WHERE relname = 'freeze_demo';

-- «Сжигатель» xid: процедура с COMMIT внутри цикла. Каждый COMMIT завершает
-- транзакцию, следующая запись берёт новый xid — так возраст растёт как в проде.
DROP TABLE IF EXISTS xid_burner;
CREATE TABLE xid_burner (n bigserial);
CREATE OR REPLACE PROCEDURE burn_xids(n int) LANGUAGE plpgsql AS $$
BEGIN
  FOR i IN 1..n LOOP
    INSERT INTO xid_burner DEFAULT VALUES;
    COMMIT;
  END LOOP;
END $$;

\echo === сжигаем 100000 транзакций — возраст freeze_demo растёт ===
-- synchronous_commit=off: не ждать fsync на каждом из 100k COMMIT (иначе очень долго).
-- На корректность xid/freeze это не влияет — влияет лишь на durability при краше.
SET synchronous_commit = off;
CALL burn_xids(100000);
RESET synchronous_commit;
SELECT age(relfrozenxid) AS xid_age_after_burn
FROM pg_class WHERE relname = 'freeze_demo';

\echo === VACUUM FREEZE: замораживает старые версии, возраст падает ===
VACUUM FREEZE freeze_demo;
SELECT age(relfrozenxid) AS xid_age_after_freeze
FROM pg_class WHERE relname = 'freeze_demo';

\echo === пороги, управляющие принудительным freeze ===
-- vacuum_freeze_min_age    — с какого возраста версию можно замораживать;
-- autovacuum_freeze_max_age — при каком возрасте autovacuum ВЫНУЖДЕН прийти (aggressive),
--                             даже если bloat нет и таблица не менялась.
SELECT name, setting
FROM pg_settings
WHERE name IN ('vacuum_freeze_min_age', 'autovacuum_freeze_max_age', 'vacuum_freeze_table_age')
ORDER BY name;

\echo === мониторинг wraparound по всем базам (за этим следят в проде) ===
-- age(datfrozenxid) близко к autovacuum_freeze_max_age (200M) — сигнал; к 2^31 — авария.
SELECT datname,
       age(datfrozenxid) AS xid_age,
       round(100.0 * age(datfrozenxid) / 2147483648, 4) AS pct_to_wraparound
FROM pg_database
ORDER BY xid_age DESC;
