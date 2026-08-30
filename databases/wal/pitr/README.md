# PostgreSQL PITR (point-in-time recovery)

Тезис #3 серии: **PITR = базовый бэкап + непрерывный WAL-архив → восстановление на любой момент между ними.**

Базовый бэкап сам по себе — это снимок данных на момент `pg_basebackup`. Чтобы откатиться на
произвольную точку *после* бэкапа (например «за минуту до ошибочного DELETE»), нужен ещё и
архив WAL-сегментов, накопленный primary между бэкапом и точкой восстановления. PostgreSQL
проигрывает архивные WAL поверх базового бэкапа и останавливается ровно там, где вы попросили.

Стенд использует уже поднятый в `../postgres/` инстанс — там уже включены
`archive_mode=on` и `archive_command`, пишущий сегменты в примонтированный `archive/`.

## Что делает `pitr-test.sh`

1. Поднимает primary (`../postgres`), чистит `archive/` от сегментов прошлых прогонов (см. «Подводные камни» ниже).
2. Снимает базовый бэкап `pg_basebackup -X stream` прямо внутри контейнера primary (`/tmp/base`).
3. `INSERT id=1` → фиксирует `TARGET=now()` → пауза → `INSERT id=2` → `pg_switch_wal()`
   (форсирует архивацию сегмента, где лежат оба коммита).
4. Копирует базовый бэкап из контейнера в staging-каталог на хосте (обычный `mktemp -d`,
   **не** будущий data-dir), кладёт туда `recovery.signal` и recovery-настройки
   (`restore_command`, `recovery_target_time=$TARGET`, `recovery_target_action=promote`).
5. Поднимает **отдельный** restore-инстанс (контейнер `pitr-restore`, порт 5443) с data-dir на
   **именованном Docker volume** (не host-bind — см. «Data-dir restore-инстанса: почему volume,
   а не host-bind» ниже) и read-only bind-томом `archive/`. Staging-каталог заливается в
   контейнер через `docker cp`, разворачивается поверх volume и овнерится уже **внутри**
   контейнера при старте.
6. PostgreSQL проигрывает архивные WAL до `TARGET` и промоутится.
7. Проверяет: на восстановленном инстансе `SELECT * FROM pitr` — видно только `id=1`,
   `count(*)=1`, `id=2` отсутствует.
8. Останавливает restore-контейнер, удаляет его volume и `docker compose down` primary.

## Data-dir restore-инстанса: почему volume, а не host-bind

Первая версия стенда монтировала data-dir restore-инстанса как обычный host-bind
(`./restore-base` под репозиторием). На части Windows/WSL-конфигураций Docker это падает:

```
FATAL:  data directory "/var/lib/postgresql/18/docker" has wrong ownership
```

PostgreSQL при старте жёстко проверяет unix-овнершип ($UID:$GID) каталога данных и требует,
чтобы им владел пользователь, от имени которого запущен процесс (`postgres`). Host-bind каталога
из репозитория, лежащего на Windows/WSL-смонтированной ФС, не всегда способен полноценно
представить unix-овнершип — `chown` на такой bind либо не проходит, либо не сохраняется так,
как это нужно PostgreSQL (в отличие, например, от файлов, которые просто читаются/пишутся —
там владелец не проверяется).

**Решение:** data-dir restore-инстанса — **именованный Docker volume**
(`pitr-restore-data`), а не host-каталог. Именованный volume живёт в собственном хранилище
Docker (реальная ФС внутри Docker/WSL2 VM), где `chown -R postgres:postgres` отрабатывает
штатно, как на любом Linux-хосте. Наполнение volume устроено так:

1. Базовый бэкап копируется из контейнера primary в обычный host-каталог (`mktemp -d`,
   staging, не data-dir — овнершип этого каталога роли не играет, это просто транзитный хоп).
2. Staging-каталог заливается в restore-контейнер (`docker cp`) в **отдельный путь вне
   $PGDATA** (`/tmp/pitr-stage` в ephemeral-слое контейнера).
3. CMD контейнера сам разворачивает `/tmp/pitr-stage` поверх volume (`cp -a ... && rm -rf
   /tmp/pitr-stage`), затем `chown -R postgres:postgres` + `chmod 0700` на самом volume —
   и только теперь запускает `postgres`.

Почему staging — **не** прямо под `$PGDATA` (например `.../docker/base`): базовый бэкап сам
содержит top-level каталог `base/` (табличное пространство по умолчанию). Если staging тоже
положить как `.../docker/base`, `cp -a base/. ..` копирует вложенный `base` (реальные данные)
на путь, совпадающий с самим staging-каталогом, а последующий `rm -rf` сносит уже
скопированные данные (поймано вживую: recovery успешно доходит до конца и промоутится, но
следующий же `psql`-коннект падает с `"base/16384" is not a valid data directory`). Staging в
не пересекающемся с $PGDATA пути (`/tmp/pitr-stage`) снимает эту коллизию.

Архив WAL primary (`../postgres/archive/`) как монтировался host-bind'ом, так и остаётся им —
но **только на чтение** (`:ro`). Разница принципиальна: `restore_command` из archive/ **читает**
файлы, а не создаёт их с нуля с конкретным unix-владельцем, поэтому Windows/WSL-специфика
host-bind тут не мешает.

Named volume убирается вместе с контейнером через `docker volume rm -f pitr-restore-data`
(или `docker compose down -v`, если бы стенд был описан через compose) — `pitr-test.sh`
делает это в `trap`-очистке и явно в конце успешного прогона.

Запуск:

```bash
cd digital-cookbook/wal/pitr
chmod +x pitr-test.sh
./pitr-test.sh
```

## Реальный вывод (прогон, data-dir на named volume)

```
recovery_target_time = 2026-07-07 08:33:19.454524+00 (после id=1, до id=2)
текущее состояние primary (ожидаем 2 строки): 2
...
2026-07-07 08:33:36.672 UTC [23] LOG:  starting point-in-time recovery to 2026-07-07 08:33:19.454524+00
2026-07-07 08:33:36.798 UTC [23] LOG:  restored log file "000000010000000000000004" from archive
2026-07-07 08:33:36.817 UTC [23] LOG:  consistent recovery state reached at 0/3000120
2026-07-07 08:33:36.817 UTC [23] LOG:  recovery stopping before commit of transaction 756, time 2026-07-07 08:33:21.775641+00
2026-07-07 08:33:37.010 UTC [23] LOG:  archive recovery complete
...
1|2026-07-07 08:33:17.118613+00
count(*) на восстановленном инстансе (ожидаем 1): 1
pg_is_in_recovery() (ожидаем f — промоутнулся): f
```

Прогонялся дважды подряд с нуля (`docker volume rm` + `docker compose down` между прогонами) —
результат идентичен оба раза, никаких остаточных volume/контейнеров после `pitr-test.sh`.

`recovery stopping before commit of transaction 756` — PostgreSQL нашёл в архивном WAL коммит
`id=2` (транзакция 756) и остановился *перед* ним, потому что его время коммита позже `TARGET`.
На восстановленном инстансе видно ровно то состояние, что было на момент `TARGET`: `id=1` есть,
`id=2` нет, инстанс уже промоутнут (не в recovery) и доступен на чтение/запись.

## Механика restore (шаги вручную, без скрипта)

Если нужно воспроизвести restore руками (не через `pitr-test.sh`) — вот шаги:

1. **Базовый бэкап** снимается на РАБОТАЮЩЕМ primary с `archive_mode=on`:
   ```bash
   docker compose exec -T postgres bash -c "pg_basebackup -U postgres -D /tmp/base -X stream"
   ```
   `-X stream` подтягивает WAL, нужный для консистентности самого бэкапа (fsync-барьер),
   но **не** WAL для точки восстановления позже момента бэкапа — тот берётся из архива.

2. **Копируем базовый бэкап** в staging-каталог на хосте (обычный temp-каталог, НЕ будущий
   data-dir — см. «Data-dir restore-инстанса» выше про то, почему это важно на Windows/WSL):
   ```bash
   docker cp <primary-container-id>:/tmp/base/. ./stage/
   ```

3. **Готовим data-dir восстановленного инстанса** — именованный Docker volume, не host-каталог:
   ```bash
   docker volume create pitr-restore-data
   ```
   - Пустой файл `recovery.signal` в корне data-dir — маркер для PG 12+: «войти в archive recovery».
     Кладём его прямо в staging-каталог (`touch ./stage/recovery.signal`) до заливки в volume.
   - `restore_command`, `recovery_target_time`, `recovery_target_action` — дописываем в
     `postgresql.auto.conf`, который уже есть в staging-каталоге после basebackup:
     ```
     restore_command = 'cp /archive/%f %p'
     recovery_target_time = '2026-07-07 08:33:19.454524+00'
     recovery_target_action = 'promote'
     ```
     `%f` — имя запрошенного WAL-файла, `%p` — путь, куда PG ждёт файл. `restore_command`
     выполняется PostgreSQL для каждого недостающего сегмента.
   - Создаём (пока не запуская) restore-контейнер с volume под data-dir и заливаем в него
     staging-каталог **не поверх $PGDATA напрямую**, а во временный путь вроде `/tmp/pitr-stage`
     (см. пояснение про коллизию имён с `base/` в разделе выше), затем при старте контейнер сам
     разворачивает его `cp -a /tmp/pitr-stage/. /var/lib/postgresql/18/docker/` поверх volume:
     ```bash
     docker create --name pitr-restore -v pitr-restore-data:/var/lib/postgresql/18/docker \
       -v "$(pwd)/../postgres/archive:/archive:ro" postgres:18.4 \
       bash -c 'cp -a /tmp/pitr-stage/. /var/lib/postgresql/18/docker/ && rm -rf /tmp/pitr-stage && \
                chown -R postgres:postgres /var/lib/postgresql/18/docker && chmod 0700 /var/lib/postgresql/18/docker && \
                exec gosu postgres postgres'
     docker cp ./stage pitr-restore:/tmp/pitr-stage
     docker start pitr-restore
     ```

4. **WAL-архив primary** (`../postgres/archive/`) монтируется в restore-контейнер **read-only**
   по пути `/archive` — именно туда стучится `restore_command` из шага 3. Это по-прежнему
   host-bind (в отличие от data-dir): чтение файла не требует смены unix-владельца, только
   на запись/`chown` натыкается ограничение Windows/WSL bind-mount.

5. **Права и запуск** (data-dir — свежий volume, изначально принадлежит root — надо отдать postgres):
   ```bash
   chown -R postgres:postgres <data-dir>   # теперь отрабатывает штатно — data-dir на volume, не bind
   chmod 0700 <data-dir>
   gosu postgres postgres   # или docker-entrypoint эквивалент; НЕ через официальный entrypoint —
                             # он попытается заново инициализировать кластер (initdb), если решит,
                             # что PGDATA "пустой"/не соответствует ожиданиям
   ```
   В `pitr-test.sh` это всё зашито в CMD контейнера (шаг 3): `cp -a` разворачивает staging,
   `chown`+`chmod` отдают владение postgres, `exec gosu postgres postgres` стартует БД —
   минуя штатный `docker-entrypoint.sh` (который иначе может попытаться инициализировать кластер).

6. **PostgreSQL проигрывает WAL** из `/archive` начиная с бэкапа, доходит до транзакции с временем
   коммита > `recovery_target_time`, останавливается **перед** ней, и благодаря
   `recovery_target_action='promote'` сразу промоутится — инстанс готов принимать запросы
   на чтение/запись с состоянием на момент `recovery_target_time`.

7. **Проверка:**
   ```sql
   SELECT * FROM pitr ORDER BY id;   -- только id=1
   SELECT count(*) FROM pitr;         -- 1
   SELECT pg_is_in_recovery();        -- f (промоутнулся)
   ```

### Важно про `$PGDATA` в образе postgres:18

Реальный `PGDATA` официального образа `postgres:18.4` — `/var/lib/postgresql/18/docker`,
**не** `/var/lib/postgresql/data`. Именованный volume под data-dir
монтируется именно на этот путь.

## Подводные камни

1. **`archive/` — общий том со стендом `../postgres/`.** Между прогонами (и между стендами серии) в нём
   остаются WAL-сегменты прошлых сессий. `archive_command` в стенде `../postgres/` —
   ```
   test ! -f /var/lib/postgresql/archive/%f && cp %p /var/lib/postgresql/archive/%f
   ```
   Он архивирует сегменты **строго по порядку** (архивер PostgreSQL однопоточный и
   последовательный). Если файл с именем следующего по очереди сегмента уже существует в
   архиве (от прошлого прогона) — `test ! -f` даёт false, вся команда возвращает exit 1,
   PostgreSQL считает архивацию сегмента проваленной и **бесконечно ретраит именно его**,
   раз в ~60 секунд, так и не добираясь до новых сегментов с актуальными данными.
   `pg_stat_archiver` в этом состоянии показывает `archived_count=0` при ненулевом
   `failed_count`, растущем с каждым ретраем. Проверено вживую (лог):
   ```
   WARNING:  archiving write-ahead log file "000000010000000000000001" failed too many times, will try again later
   LOG:  archive command failed with exit code 1
   DETAIL:  The failed archive command was: test ! -f /var/lib/postgresql/archive/000000010000000000000001 && cp ...
   ```
   **Решение:** `pitr-test.sh` чистит `archive/*` в самом начале прогона — это demo-артефакт,
   не данные, пересоздаётся каждый раз архивацией с нуля.

2. **Git Bash на Windows (MSYS) переписывает пути в `-v`/`docker cp` докера.** Путь вида
   `G:/7/Projects/...`, переданный в `docker create -v ...`, MSYS конвертирует в POSIX-путь и
   затем обратно — в результате получается что-то вроде `C:\Program Files\Git\7\Projects\...`
   (mount мимо цели, контейнер стартует с пустым/чужим каталогом). Лечится `MSYS_NO_PATHCONV=1`
   **только** для конкретного `docker create` (для `docker cp` эта переменная, наоборот, мешает —
   там пути не параметр `-v`, а часть команды, поэтому в скрипте `MSYS_NO_PATHCONV=1` выставлен
   инлайн перед одной командой `docker create`, а не глобально через `export`).

   Отдельно от этого поймано вживую: `docker cp SRC_HOST_DIR/. CONTAINER:/dest/` (с трейлинг-
   точкой на ХОСТОВОЙ стороне пути — обычный способ попросить docker cp «скопировать
   содержимое каталога, а не сам каталог») **теряет** эту точку при MSYS-конвертации хостового
   пути и в результате кладёт вложенную папку вместо flatten-копии. Решение — не полагаться на
   этот триггер docker cp через MSYS вообще: `pitr-test.sh` копирует базовый бэкап в контейнер
   как обычный подкаталог (`docker cp ./stage CONTAINER:/tmp/pitr-stage`, путь назначения ещё
   не существует — однозначная семантика без трейлинг-точки), а разворачивание поверх
   $PGDATA (`cp -a /tmp/pitr-stage/. ...`) делает сам контейнер изнутри, обычным coreutils
   `cp`, не докером — там MSYS уже ни при чём.

3. **`recovery_target_time` должен строго попадать между коммитами.** TARGET фиксируется через
   `SELECT now()` на primary *после* `INSERT id=1` и *до* `INSERT id=2` с секундными паузами
   по обе стороны — иначе есть риск, что оба INSERT окажутся по одну сторону от TARGET из-за
   таймингов контейнера/checkpoint.

4. **Сеть docker-compose при `down`.** Restore-контейнер подключён к сети `postgres_default`,
   создаваемой `docker compose` для primary. Если не удалить restore-контейнер до
   `docker compose down`, `down` не может снести сеть («Resource is still in use») — в скрипте
   `pitr-restore` останавливается явно перед `docker compose down`.

5. **Host-bind как data-dir PostgreSQL на Windows/WSL — ненадёжен.** См. отдельный раздел
   «Data-dir restore-инстанса: почему volume, а не host-bind» выше: `chown` на data-каталог,
   лежащий на Windows/WSL-смонтированной ФС, не всегда отрабатывает так, как это нужно
   PostgreSQL, что приводит к `FATAL: data directory ... has wrong ownership`. Урок общего
   плана: bind-mount с хоста годится для *чтения* файлов (конфиги, WAL-архив, статика) —
   но не годится как data-каталог сервиса, который сам управляет unix-правами на своих файлах
   (PostgreSQL — не единственный такой; та же оговорка касается, например, etcd, Cassandra
   и любой БД, которая делает `chown`/строгую проверку прав на PGDATA-подобный каталог).
   Именованный Docker volume для такого каталога — не «на всякий случай», а системное решение.

6. **`docker cp` не копирует между двумя контейнерами напрямую.** `docker cp CONTAINER1:/path
   CONTAINER2:/path` падает с `copying between containers is not supported`. Обходной путь —
   через host как промежуточный хоп (`docker cp CONTAINER1:/path ./stage && docker cp ./stage
   CONTAINER2:/path`); `./stage` в этом случае не data-dir и требований к unix-овнершипу к
   нему не относится — обычный временный каталог.

## Файлы

- `pitr-test.sh` — полный прогон: primary → basebackup → изменения → архивация →
  restore до точки в отдельном контейнере (data-dir на named Docker volume) → проверка → очистка.
- Runtime-артефакты (staging-каталог с базовым бэкапом, named volume `pitr-restore-data`) —
  создаются в `mktemp -d`/Docker и удаляются скриптом (`trap` + явная очистка в конце), в
  репозитории не появляются вовсе — отдельный `.gitignore` для них больше не нужен.
