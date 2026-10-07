--liquibase formatted sql

--changeset smig:1
CREATE TABLE probe (id INT PRIMARY KEY, name TEXT);
--rollback DROP TABLE probe;

--changeset smig:2
ALTER TABLE probe ADD COLUMN note TEXT;
--rollback ALTER TABLE probe DROP COLUMN note;
