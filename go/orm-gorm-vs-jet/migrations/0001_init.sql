CREATE TABLE users (
    id   bigserial PRIMARY KEY,
    name text NOT NULL
);

CREATE TABLE posts (
    id             bigserial PRIMARY KEY,
    user_id        bigint NOT NULL REFERENCES users(id),
    title          text   NOT NULL,
    metadata       jsonb  NOT NULL DEFAULT '{}',
    likes          int    NOT NULL DEFAULT 0,
    comments_count int    NOT NULL DEFAULT 0
);

CREATE TABLE tags (
    id   bigserial PRIMARY KEY,
    name text NOT NULL UNIQUE
);

CREATE TABLE post_tags (
    post_id bigint REFERENCES posts(id),
    tag_id  bigint REFERENCES tags(id),
    PRIMARY KEY (post_id, tag_id)
);

-- немного данных для примеров
INSERT INTO users (name) VALUES ('alice'), ('bob');
INSERT INTO posts (user_id, title, metadata, likes) VALUES
    (1, 'Hello',   '{"source":"blog"}',    3),
    (1, 'Second',  '{"source":"import"}',  0),
    (2, 'Bob post','{"source":"blog"}',   10);
