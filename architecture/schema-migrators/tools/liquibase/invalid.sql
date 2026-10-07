--liquibase formatted sql

--changeset smig:1
CREATE TABLE broken (id INT PRIMARY KEY,, name TEXT);
--rollback DROP TABLE broken;
