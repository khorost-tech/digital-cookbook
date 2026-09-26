-- Установка расширений и проверка версий. Таблицы создаёт каждый сценарий сам
-- (эксперименты с VACUUM/bloat мутируют данные по-разному — держим их независимыми).

CREATE EXTENSION IF NOT EXISTS pgstattuple;  -- измерение bloat (contrib)
CREATE EXTENSION IF NOT EXISTS pg_repack;    -- онлайн-переупаковка (внешнее расширение)

\echo === версии ===
SELECT version();
SELECT name, default_version, installed_version
FROM pg_available_extensions
WHERE name IN ('pgstattuple', 'pg_repack')
ORDER BY name;
