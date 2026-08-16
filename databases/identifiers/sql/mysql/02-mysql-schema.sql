-- Артефакт 2 (cross-engine): MySQL/InnoDB-половина контраста.
-- Две таблицы, идентичные КРОМЕ ширины первичного ключа. InnoDB кластеризует
-- таблицу по PK -> вторичный индекс sx(payload) хранит в каждом листе
-- (payload, PK). Ширина PK ПОПАДАЕТ в размер вторичного индекса — в отличие
-- от PostgreSQL (heap не кластеризована, вторичный btree хранит ctid).
CREATE TABLE m_bigint (
  id      BIGINT AUTO_INCREMENT PRIMARY KEY,
  payload VARCHAR(64) NOT NULL,
  KEY sx (payload)
) ENGINE=InnoDB;

CREATE TABLE m_uuid (
  id      BINARY(16) PRIMARY KEY,           -- 16-байтный кластеризованный PK
  payload VARCHAR(64) NOT NULL,
  KEY sx (payload)
) ENGINE=InnoDB;
