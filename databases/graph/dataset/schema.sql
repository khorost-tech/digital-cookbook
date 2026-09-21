-- Реляционная модель того же графа — для honest baseline на рекурсивных CTE.
-- Идентификаторы узлов глобально уникальны (единый счётчик по всем типам),
-- поэтому рёбра между разными типами узлов ссылаются на id без указания типа.
-- Индексы стоят на всех колонках, по которым идёт обход: иначе baseline медленный
-- из-за отсутствия индекса, а не из-за реляционной модели — сравнение было бы нечестным.

DROP TABLE IF EXISTS interacts, owns, depends_on, collaborates, grants, has_role, member_of CASCADE;
DROP TABLE IF EXISTS packages, services, resources, roles, teams, users CASCADE;

-- ── узлы ─────────────────────────────────────────────────────────────────────
CREATE TABLE users     (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE teams     (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE roles     (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE resources (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE services  (id bigint PRIMARY KEY, name text NOT NULL);
CREATE TABLE packages  (id bigint PRIMARY KEY, name text NOT NULL);

-- ── рёбра ────────────────────────────────────────────────────────────────────

-- пользователь состоит в команде
CREATE TABLE member_of (
    user_id bigint NOT NULL,
    team_id bigint NOT NULL,
    PRIMARY KEY (user_id, team_id)
);

-- у субъекта (пользователя или команды) есть роль
CREATE TABLE has_role (
    subject_id   bigint NOT NULL,
    subject_kind text   NOT NULL CHECK (subject_kind IN ('user','team')),
    role_id      bigint NOT NULL,
    PRIMARY KEY (subject_id, subject_kind, role_id)
);

-- роль даёт доступ к ресурсу
CREATE TABLE grants (
    role_id     bigint NOT NULL,
    resource_id bigint NOT NULL,
    PRIMARY KEY (role_id, resource_id)
);

-- пользователи сотрудничают (ненаправленное ребро; хранится одной канонической строкой a_id < b_id,
-- рекурсивный CTE обходит его симметрично: WHERE a_id = x OR b_id = x — чтобы счётчик рёбер
-- совпадал с Neo4j/AGE, где связь одна)
CREATE TABLE collaborates (
    a_id bigint NOT NULL,
    b_id bigint NOT NULL,
    PRIMARY KEY (a_id, b_id)
);

-- зависимость сервиса от сервиса или пакета от пакета
CREATE TABLE depends_on (
    src_id bigint NOT NULL,
    dst_id bigint NOT NULL,
    kind   text   NOT NULL CHECK (kind IN ('service','package')),
    PRIMARY KEY (src_id, dst_id)
);

-- команда владеет сервисом
CREATE TABLE owns (
    team_id    bigint NOT NULL,
    service_id bigint NOT NULL,
    PRIMARY KEY (team_id, service_id)
);

-- пользователь взаимодействовал с ресурсом (для рекомендаций и fraud-колец)
CREATE TABLE interacts (
    user_id     bigint NOT NULL,
    resource_id bigint NOT NULL,
    PRIMARY KEY (user_id, resource_id)
);

-- ── индексы обхода ────────────────────────────────────────────────────────────
CREATE INDEX idx_member_of_user   ON member_of (user_id);
CREATE INDEX idx_member_of_team   ON member_of (team_id);
CREATE INDEX idx_has_role_subject ON has_role (subject_id, subject_kind);
CREATE INDEX idx_has_role_role    ON has_role (role_id);
CREATE INDEX idx_grants_role      ON grants (role_id);
CREATE INDEX idx_grants_resource  ON grants (resource_id);
CREATE INDEX idx_collab_a         ON collaborates (a_id);
CREATE INDEX idx_collab_b         ON collaborates (b_id);
CREATE INDEX idx_depends_src      ON depends_on (src_id);
CREATE INDEX idx_depends_dst      ON depends_on (dst_id);
CREATE INDEX idx_owns_team        ON owns (team_id);
CREATE INDEX idx_owns_service     ON owns (service_id);
CREATE INDEX idx_interacts_user   ON interacts (user_id);
CREATE INDEX idx_interacts_res    ON interacts (resource_id);
