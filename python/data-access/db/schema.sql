-- Схема стенда: три таблицы со связями один-ко-многим.
--
-- Выбор именно такой формы не случаен. N+1 возникает естественно там, где
-- есть родитель и коллекция потомков, и его нельзя показать на одной
-- таблице, не подстроив задачу под ответ. Два уровня вложенности
-- (автор -> книги -> отзывы) нужны, чтобы отличить N+1 от N+1+M: у
-- joinedload на двух уровнях цена растёт произведением, а не суммой, и это
-- как раз тот компромисс, который обычно узнают в проде.

DROP TABLE IF EXISTS reviews;
DROP TABLE IF EXISTS books;
DROP TABLE IF EXISTS authors;

CREATE TABLE authors (
    id          integer PRIMARY KEY,
    name        text        NOT NULL,
    country     text        NOT NULL,
    born_at     date        NOT NULL
);

CREATE TABLE books (
    id          integer PRIMARY KEY,
    author_id   integer     NOT NULL REFERENCES authors(id),
    title       text        NOT NULL,
    published   date        NOT NULL,
    price       numeric(8,2) NOT NULL,
    pages       integer     NOT NULL
);

CREATE TABLE reviews (
    id          integer PRIMARY KEY,
    book_id     integer     NOT NULL REFERENCES books(id),
    rating      smallint    NOT NULL,
    body        text        NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE INDEX books_author_id_idx ON books (author_id);
CREATE INDEX reviews_book_id_idx ON reviews (book_id);

-- Индекса по books.pages нет НАМЕРЕННО: на нём стоит запрос heavy-sql,
-- где работать должна база, а не клиент. Появится индекс — исчезнет
-- сценарий, в котором цена слоя доступа обязана утонуть.
