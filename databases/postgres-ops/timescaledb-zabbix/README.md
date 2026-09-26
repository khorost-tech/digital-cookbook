# TimescaleDB под Zabbix: конвертация history/trends + тюнинг компрессии

Sanitized-runbook к статье «TimescaleDB под Zabbix: как перестать вручную бороться с bloat»
(серия «PostgreSQL в проде»). Внутренние хосты, IP, имена стенда и стан­зы убраны —
подставьте свои. Это **не** docker-стенд: кейс воспроизводится на живой связке
Zabbix + TimescaleDB + PostgreSQL (в проде — под Patroni), одной командой не поднимается.
Здесь — процедура, проверочные запросы и ожидаемые выводы, которые можно адаптировать.

Версии кейса: PostgreSQL 18, TimescaleDB **2.27.2** (потолок для Zabbix 7.4.12; пин обязателен),
Zabbix **7.4.12** (поддержка TimescaleDB 2.27.X добавлена только с 7.4.12).

## 0. Точка отката

Перед конвертацией — свежий бэкап (в кейсе pgBackRest incr поверх ежедневного full):

    pgbackrest --stanza=<stanza> --type=incr backup
    pgbackrest --stanza=<stanza> info | tail -20     # свежесть смотреть в ХВОСТЕ вывода

После конвертации (шаг 3) единственный полноценный откат — восстановление из этого бэкапа.

## 1. Пин версии TimescaleDB (обязателен)

В репозитории может лежать более свежая 2.28.x — она выше потолка Zabbix 7.4.12 и не
поддерживается. Пин через APT priority (`Pin-Priority: 1001`) + `apt-mark hold`:

    apt-cache policy timescaledb-2-postgresql-18   # Candidate должен быть 2.27.*, не 2.28.*

На всех нодах кластера версия обязана совпадать (иначе реплика не стартует):

    dpkg-query -W -f='${Package} ${Version} ${Status}\n' \
      timescaledb-2-postgresql-18 timescaledb-2-loader-postgresql-18
    # ожидать на всех: 2.27.2~...  "hold ok installed"

## 2. shared_preload_libraries + rolling restart

    # добавить timescaledb в shared_preload_libraries (в проде — через patronictl edit-config)
    # рестарт ПО ОДНОЙ ноде: сначала реплики, лидер ПОСЛЕДНИМ
    SHOW shared_preload_libraries;   # ожидать: ...,timescaledb на каждой ноде

## 3. Конвертация в гипертаблицы (точка невозврата)

**Остановить zabbix-server на время конвертации** — `create_hypertable` берёт ACCESS EXCLUSIVE,
а Zabbix непрерывно пишет в те же таблицы. Скрипт конвертации поставляется самим Zabbix:

    # на лидере, от postgres, через локальный сокет:
    CREATE EXTENSION IF NOT EXISTS timescaledb;
    SELECT extversion FROM pg_extension WHERE extname='timescaledb';   -- ожидать 2.27.2

    # прогнать schema.sql из /usr/share/zabbix/sql-scripts/postgresql/timescaledb/
    # (~9 вызовов create_hypertable, всё в ОДНОЙ транзакции, ~6-7 мин, migrate_data)
    psql -d zabbix -v ON_ERROR_STOP=1 -f schema.sql
    # успех = NOTICE: TimescaleDB is configured successfully
    # WARNING про "character varying does not follow best practices" — косметика, игнор

`schema.sql` сам выставляет `db_extension='timescaledb'`, `compression_status=1`,
`compress_older='7d'`, `hk_*_global=1` — отдельный UI-шаг для компрессии не нужен.

Проверки после запуска Zabbix:

    -- гипертаблицы (ожидать 9) и настройки
    SELECT hypertable_name, num_chunks FROM timescaledb_information.hypertables ORDER BY 1;
    SELECT name, value_str, value_int FROM settings
    WHERE name IN ('db_extension','compression_status','compress_older','hk_history','hk_trends');
    -- данные пишутся: два замера с паузой, max(clock) должен расти
    SELECT max(clock) FROM history;

**Разовый эффект конвертации (кейс):** БД `zabbix` 13–14 ГБ → 8.2 ГБ ещё до компрессии.

| таблица | было | после конвертации |
|---|---|---|
| `history` | 10 ГБ | 6.0 ГБ |
| `history_uint` | 2.6 ГБ | 1.6 ГБ |
| `trends` | 517 МБ | 256 МБ |

## 4. Диагностика ловушки «порог сжатия = окно ретеншна»

Симптом: `trends`/`trends_uint` сжаты на 93–96%, а `history` (самая большая) — **0 сжатых чанков**,
хотя `compression_status=1` и задания `policy_compression` идут `Success`.

    -- сжато/несжато по чанкам гипертаблицы
    SELECT hypertable_name, count(*) FILTER (WHERE is_compressed) AS compressed, count(*) AS total
    FROM timescaledb_information.chunks GROUP BY 1 ORDER BY 1;
    -- порог компрессии по политикам против окна ретеншна
    SELECT hypertable_name, config->>'compress_after' AS compress_after
    FROM timescaledb_information.jobs WHERE proc_name='policy_compression' ORDER BY 1;

Причина: `compress_older=7d` = `hk_history=7d`, чанк history суточный → он годен к сжатию ровно
тогда, когда его дропает ретеншн. `trends` жмётся (chunk 30 дней, `hk_trends=365d` ≫ 7d).

## 5. Обход: compress_older ниже 7 дней (НЕ поддерживается Zabbix)

> ⚠️ Zabbix документирует **минимум 7 дней** для «Compress records older than». Значение ниже —
> неподдерживаемый хак, ставится только прямой правкой БД в обход UI-валидации. На **7.4.12**
> сработало; на другой версии Zabbix может валидировать порог сервером и вернуть 7 дней. Проверять.

`compress_older` — глобальная Zabbix-managed настройка в `settings`; политику TimescaleDB
(`compress_after`) Zabbix переприменяет сам → править `alter_job` бесполезно, вернёт своё.

    -- понизить порог (обход UI-минимума)
    UPDATE settings SET value_str='3d' WHERE name='compress_older';

    # переприменить и форснуть housekeeper (на zabbix-server):
    zabbix_server -R config_cache_reload
    zabbix_server -R housekeeper_execute

    -- проверить, что политики подхватили (ожидать 266400 = 3.08 дня вместо 612000 = 7.08 дня)
    SELECT hypertable_name, config->>'compress_after'
    FROM timescaledb_information.jobs WHERE proc_name='policy_compression' ORDER BY 1;

    -- прогнать компрессию сразу, не ждать суточного цикла:
    SELECT job_id FROM timescaledb_information.jobs WHERE proc_name='policy_compression';
    CALL run_job(<job_id>);   -- для каждого job_id

**Результат обхода (кейс):** `history` 6.3 → 3.7 ГБ (4/8 сжато), `history_uint` 1.7 → 0.98 ГБ,
БД `zabbix` **8.8 → 5.4 ГБ (−39%)**. Компрессия реплицируется (реплики тоже 5.4 ГБ).

## Грабли (проверено на практике)

1. **Пин версии обязателен** — без него apt возьмёт 2.28.x выше потолка Zabbix 7.4.
2. **Диск после конвертации временно РАСТЁТ** — это `pg_wal` (8–12 ГБ от переписывания данных),
   не сами данные; уходит по мере архивации WAL. Следить за headroom.
3. **`set -o pipefail` + `grep -q` = rc 141 (SIGPIPE)** при успешном совпадении — ложное падение
   assert'ов в автоматизации. Проверять через `apt-cache policy ... | grep 'Candidate:\s+2\.27\.'`.
4. **Веб-фронтенд Zabbix долбит БД**, даже когда zabbix-server остановлен — его `SELECT`ы к
   `history` висят на локе во время конвертации. Безвредно.
5. **Обход `compress_older<7d` — опциональная оптимизация, не решение bloat.** Решение — сам
   переход на гипертаблицы/`drop_chunks`. Обход дожимает history и оправдан, только если history
   реально давит на диск (в кейсе — не давил).
