-- Многорегиональная схема.
--
-- PRIMARY REGION и ADD REGION требуют, чтобы узлы были запущены с
-- --locality: без этого команды отвергаются.
--
-- LOCALITY REGIONAL BY ROW означает, что у каждой строки есть свой
-- регион, и её диапазон живёт рядом с этим регионом.

CREATE DATABASE IF NOT EXISTS geo PRIMARY REGION "ru-central";

ALTER DATABASE geo ADD REGION IF NOT EXISTS "ru-west";
ALTER DATABASE geo ADD REGION IF NOT EXISTS "ru-east";

USE geo;

CREATE TABLE IF NOT EXISTS users (
    id    UUID NOT NULL DEFAULT gen_random_uuid(),
    email STRING NOT NULL,
    note  STRING,
    PRIMARY KEY (id)
) LOCALITY REGIONAL BY ROW;
