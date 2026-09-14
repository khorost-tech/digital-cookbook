-- Очередь задач одной таблицей. Ключевое отличие от «таблицы сообщений» —
-- поля аренды (leased_until/leased_by): джоба выполняется минутами, воркер может
-- умереть посередине, поэтому владение джобой имеет СРОК, а не только факт.
CREATE TABLE jobs (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT        NOT NULL,
    payload      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- queued -> running -> done | failed
    state        TEXT        NOT NULL DEFAULT 'queued',
    run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt      INT         NOT NULL DEFAULT 0,
    max_attempts INT         NOT NULL DEFAULT 3,
    -- Аренда: до какого момента джоба принадлежит воркеру leased_by.
    -- NULL у queued/done/failed; заполнены только у running.
    leased_until TIMESTAMPTZ,
    leased_by    TEXT,
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- updated_at обновляется ЯВНО в каждом UPDATE (claim/heartbeat/complete/fail/
    -- reclaim), а не триггером BEFORE UPDATE. Причина: очередь — горячая таблица,
    -- heartbeat идёт по каждой выполняющейся джобе несколько раз в минуту, и
    -- триггер на этом пути стоил бы заметно. Плата за решение — дисциплина:
    -- забытый updated_at в новом запросе тихо испортит наблюдаемость, поэтому
    -- он выставляется во ВСЕХ мутациях без исключения.
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT jobs_state_chk CHECK (state IN ('queued','running','done','failed')),
    -- Инвариант аренды закреплён СХЕМОЙ, а не дисциплиной прикладного кода:
    -- аренда существует только у running-джобы. Без этого «queued с чужой
    -- арендой» или «running без срока» пролезли бы молча — и reclaim, который
    -- ищет истёкшие аренды среди running, либо пропустил бы такую строку, либо
    -- вернул бы в очередь джобу, которую никто не брал.
    CONSTRAINT jobs_lease_chk CHECK (
        (state =  'running' AND leased_until IS NOT NULL AND leased_by IS NOT NULL)
     OR (state <> 'running' AND leased_until IS     NULL AND leased_by IS     NULL)
    )
);

-- Частичный индекс под горячий запрос claim: только queued-строки, по порядку выборки.
CREATE INDEX jobs_claim_idx ON jobs (run_at, id) WHERE state = 'queued';
-- Частичный индекс под reclaim: только running, по сроку аренды.
CREATE INDEX jobs_lease_idx ON jobs (leased_until) WHERE state = 'running';

-- Канал для LISTEN/NOTIFY (артефакт 8): триггер будит воркеров вместо polling.
CREATE OR REPLACE FUNCTION notify_job_enqueued() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('jobs_new', NEW.id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER jobs_notify_ins
    AFTER INSERT ON jobs
    FOR EACH ROW EXECUTE FUNCTION notify_job_enqueued();

-- Второй триггер обязателен, и вот почему. Джоба становится доступной не только
-- при вставке: реклейм истёкшей аренды и возврат после неудачной попытки делают
-- её queued через UPDATE. Если будить воркеров только на INSERT, о возвращённой
-- джобе они узнают лишь следующим опросом — то есть LISTEN/NOTIFY молча
-- деградирует до polling ровно в том сценарии, ради которого писалась аренда.
CREATE TRIGGER jobs_notify_requeue
    AFTER UPDATE OF state ON jobs
    FOR EACH ROW
    WHEN (NEW.state = 'queued' AND OLD.state IS DISTINCT FROM 'queued')
    EXECUTE FUNCTION notify_job_enqueued();
