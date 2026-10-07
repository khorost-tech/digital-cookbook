--liquibase formatted sql

--changeset smig:1
CREATE TABLE probe (id INT PRIMARY KEY, name TEXT);
--rollback DROP TABLE probe;

--changeset smig:2
ALTER TABLE probe ADD COLUMN note TEXT;
INSERT INTO missing_table VALUES (1);
--rollback ALTER TABLE probe DROP COLUMN note;
