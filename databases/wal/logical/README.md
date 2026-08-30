# Логический слот PostgreSQL + Go-потребитель (pglogrepl)

Стенд к статье «WAL на службе: репликация, CDC, PITR» (#3). Go-приложение
подключается к логическому слоту репликации PostgreSQL (`pgoutput`) и печатает
декодированные изменения (`INSERT`/`UPDATE`/`DELETE`) построчно, по мере их
поступления — минимальный самодельный CDC-консьюмер без брокера сообщений
посередине.

## Требования

- Поднятый стенд `wal/postgres` (`postgres:18.4`, host-порт `5433`, БД `waldemo`,
  `wal_level=logical`).
- Go (для локального запуска) или Docker (для запуска в контейнере — см. ниже).

## 1. Поднять PostgreSQL и настроить слот

```bash
cd ../postgres
docker compose up -d
sleep 8
docker compose exec -T postgres psql -U postgres -d waldemo -f - < ../logical/setup.sql
```

`setup.sql` создаёt:
- таблицу `orders (id, amount, status)`;
- publication `wal_pub` для таблицы `orders`;
- логический слот `wal_slot` с плагином вывода `pgoutput`.

## 2. Запустить потребителя

DSN по умолчанию — `postgres://postgres:waldemo@localhost:5433/waldemo?replication=database`.
Переопределяется переменной окружения `WAL_DSN`.

### Вариант А — на хосте (если хостовый firewall не блокирует порт 5433)

```bash
cd go
go run .
```

### Вариант Б — в контейнере на сети стенда (если хост не может достучаться до :5433)

На некоторых машинах host-firewall блокирует исходящие TCP-соединения именно от
Go-процессов к опубликованным Docker-портам (наблюдалось и в стенде db-indexes ORM).
Симптом: `go run .` виснет на `dial tcp 127.0.0.1:5433: ... did not properly respond`,
хотя `psql` внутри контейнера и обычный `bash` TCP-тест работают. Обход — запустить
Go прямо в сети docker-compose стенда:

```bash
cd go
docker run --rm --network postgres_default \
  -v "$(pwd):/app" -w /app \
  -e WAL_DSN="postgres://postgres:waldemo@postgres:5432/waldemo?replication=database" \
  golang:1-alpine sh -c "go run ."
```

(имя сети — `<каталог-compose>_default`, здесь `postgres_default`; проверить: `docker network ls`).

**Важно про тайминг:** после `START_REPLICATION` движку логического декодирования
PostgreSQL иногда требуется до 10–20 секунд на прогрев ("logical decoding found
consistent point"), особенно сразу после пересоздания слота. Дайте потребителю
поработать хотя бы 15–20 секунд, прежде чем вносить изменения — иначе события
могут не попасть в окно наблюдения демо-прогона.

## 3. Внести изменения и увидеть декодированный вывод

В отдельном терминале (или через `docker compose exec`):

```bash
docker compose exec -T postgres psql -U postgres -d waldemo -c \
  "INSERT INTO orders(amount,status) VALUES (100,'new'),(250,'paid'); UPDATE orders SET status='shipped' WHERE id=1;"
```

Потребитель печатает:

```
Insert public.orders: id=1, amount=100, status=new
Insert public.orders: id=2, amount=250, status=paid
Update public.orders: id=1, amount=100, status=shipped
```

(строка `Relation: public.orders (columns: ...)` в логе — служебное сообщение
pgoutput, приходит перед первым изменением по таблице в рамках сессии; печатается
через `log.Printf`, то есть уходит в stderr, а сами Insert/Update — в stdout).

## Как это устроено

- Подключение — низкоуровневое, через `pgconn.Connect` с DSN
  `?replication=database` (обязательный параметр — без него `START_REPLICATION`
  не разрешён).
- `pglogrepl.IdentifySystem` даёт текущий LSN сервера — с него стартует стрим.
- `pglogrepl.StartReplication` с `proto_version '1'` и `publication_names 'wal_pub'`.
- Цикл `conn.ReceiveMessage` разбирает `CopyData`: `PrimaryKeepaliveMessage` ('k')
  требует ответа `SendStandbyStatusUpdate`, если сервер попросил (`ReplyRequested`)
  или раз в 10 секунд по таймеру — иначе PostgreSQL может счесть клиента
  неактивным; `XLogData` ('w') парсится через `pglogrepl.ParseXLogData`, затем
  `pglogrepl.Parse` возвращает типизированное сообщение.
- Ключевой момент протокола pgoutput: `RelationMessage` приходит **до** первого
  `InsertMessage`/`UpdateMessage` по таблице — имена таблицы и колонок
  запоминаются в `map[uint32]*pglogrepl.RelationMessage` по `RelationID` и
  используются при декодировании кортежей (`Tuple.Columns` для Insert,
  `NewTuple.Columns` для Update). Тип колонки `'t'` — текстовое представление
  значения (`string(col.Data)`); это единственный формат, который присылает
  pgoutput в `proto_version 1`.

## Остановка и очистка

```bash
cd ../postgres
docker compose down
```

Слот `wal_slot` живёт вместе с volume контейнера; при следующем `docker compose up`
на чистом volume `setup.sql` пересоздаёт его с нуля.

## Версии (зафиксировано при разработке)

- Go: 1.26.3 (образ `golang:1-alpine` внутри контейнера — тоже линейка Go 1.x)
- `github.com/jackc/pgx/v5`: v5.10.0
- `github.com/jackc/pglogrepl`: v0.0.0-20260401131349-e37c41485510
- PostgreSQL: 18.4
