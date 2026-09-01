# FIXTURES — реальные выводы прогонов стенда databases/citus

Единственный источник чисел для статьи «Шардирование на практике»
(`databases/sharding-in-production`). Всё, чего здесь нет, в статье
появиться не может; всё, что здесь есть, получено живым прогоном на этой
машине (Windows 11 + WSL2 + Docker, координатор проброшен на
`localhost:15435`), а не переписано из отчётов задач 1–6.

**Дата прогона: 2026-07-20.** Стенд к моменту записи уже был поднят и
наполнен (`bash scripts/up.sh`, кластер на ДВУХ воркерах), состояние
проверено перед прогоном и после каждого артефакта:

```
$ SELECT nodeid, nodename, isactive, groupid FROM pg_dist_node ORDER BY nodeid;
 nodeid | nodename | isactive | groupid
--------+----------+----------+---------
      1 | citus-w1 | t        |       1
      2 | citus-w2 | t        |       2
(2 rows)

$ SELECT count(*) FROM pg_dist_cleanup;
0
```

Ни осиротевших записей в `pg_dist_cleanup`, ни третьего узла — стенд в
исходном состоянии. Все пять артефактов прогнаны одним проходом, в порядке
`scatter-gather-demo.sh → colocation-demo.sh → reference-demo.sh →
pagination-demo.sh → rebalance-demo.sh`, без пересборки БД между ними —
это порядок, в котором их и задумано демонстрировать (от простого
рассказа про один шард к пересборке кластера). Каждый скрипт завершился
кодом `0`. После всех пяти прогонов состояние кластера сверено ещё раз —
см. раздел «Состояние стенда после прогона» в конце файла.

## Версии (сняты командами)

| Компонент | Версия | Источник |
|---|---|---|
| Расширение Citus | `citus \| 14.1-1` | `docker exec citus-coord psql -U shard -d shard -c "SELECT extname, extversion FROM pg_extension WHERE extname='citus';"` |
| PostgreSQL | `PostgreSQL 17.6 (Debian 17.6-2.pgdg13+1) on x86_64-pc-linux-gnu, compiled by gcc (Debian 14.2.0-19) 14.2.0, 64-bit` | `docker exec citus-coord psql -U shard -d shard -c "SELECT version();"` |
| Образ | `citusdata/citus:14.1.0-pg17` | `docker inspect citus-coord --format '{{.Config.Image}}'` |

**Образ под PostgreSQL 18 существует и готов — это НЕсуффиксированный тег
`citusdata/citus:14.1.0`.** Проверено по метаданным образа:

```
$ docker inspect citusdata/citus:14.1.0 --format '{{range .Config.Env}}{{println .}}{{end}}'
PG_MAJOR=18
PG_VERSION=18.4-1.pgdg13+1
CITUS_VERSION=14.1.0.citus-1
```

Отдельного тега `14.1.0-pg18` нет именно потому, что 18-я ветка лежит в
ОСНОВНОМ теге. Суффиксы `-pg16` / `-pg17` обозначают СТАРШИЕ ветки
PostgreSQL, а не «самую новую поддерживаемую» — читать их наоборот
неправильно. Ранняя редакция этого файла утверждала, что образа под PG18 нет
и его пришлось бы собирать самостоятельно; **это было неверно и исправлено.**

**Стенд пинован на `14.1.0-pg17` сознательно**, а не вынужденно: замеры
делаются на одной фиксированной версии PostgreSQL, чтобы числа между
прогонами и между артефактами были сопоставимы. Отсюда PostgreSQL 17 в
замерах.

Расширение Citus 14.1 поддерживает PostgreSQL 16, 17 и 18 — это заявлено в
release notes (https://www.citusdata.com/updates/v14-1/) и подтверждается
существованием образа с `PG_MAJOR=18`. Формулировка «Citus не поддерживает
PostgreSQL 18» НЕВЕРНА и в статью попасть не может.

**Документация отстаёт отдельно от этого.** docs.citusdata.com и под
`stable`, и под `latest` отдаёт 13.0.1; версионированного пути `/en/v14.1/`
не существует (404). Ссылаться на `/en/stable/` как на описание 14.1 —
ошибка; в статье ссылки помечены веткой 13.0 явно.

## Окружение

- Топология: координатор (`citus-coord`) + два воркера (`citus-w1`,
  `citus-w2`), все три контейнера — на одном физическом хосте (Docker
  Desktop + WSL2, Windows 11).
- Порт хоста координатора: **15435** (`postgresql://shard:shard@localhost:15435/shard`).
  Воркеры **наружу не публикуются** намеренно — к шардированному кластеру
  положено ходить только через координатор, стенд не должен предлагать
  обходной путь.
- Артефакт 5 (`rebalance-demo.sh`) временно поднимает **третий** воркер
  (`citus-w3`, compose-профиль `grow`) и сам же убирает его по завершении —
  подробности в README стенда.
- Наполнение: `customers` 200 строк, `orders` 4000 (по ~20 на покупателя),
  `shipments` 4000 (1:1 к заказам), `carriers` 5 строк (референсная).
  Артефакты 4 и (частично) 3 добавляют собственные временные объекты
  (`orders_big` на 1 000 000 строк, `carriers_sharded`) и удаляют их сами.

**⚠️ Главная оговорка, действующая для ВСЕХ пяти артефактов.** Все узлы
кластера живут на одном физическом хосте, сетевой задержки между ними
практически нет. На настоящем кластере, где узлы разнесены по стойкам/AZ,
добавятся сетевой объём и удалённые взаимодействия между узлами — и они не
бесплатны. **Величина этого эффекта здесь НЕ измерена, и направление
изменения разрыва по времени утверждать нельзя.** Строки передаются между
узлами потоком/пакетами, а не отдельным round-trip на строку, поэтому
арифметика «строка × RTT» неверна. Из однохостового прогона переносится
структура, не время.
Из прогонов этого файла в статью можно переносить:

- **характер зависимости** (растёт/не растёт, в какую сторону);
- **структурные величины**, не зависящие от скорости сети и диска:
  `Task Count`, наличие/число узлов `MapMergeJob`, число шардов и
  размещений (`pg_dist_shard`/`pg_dist_placement`), число строк, поднятых
  координатором с шардов;

и **нельзя** переносить как ожидаемые на реальном кластере — абсолютные
миллисекунды и (кроме одного специально оговорённого случая — роста
медианы серии Б в артефакте 4, см. раздел «Артефакт 4») кратности
времени.

**⚠️ Вторая главная оговорка, действующая для ВСЕХ пяти артефактов:
сравнение Citus с обычным (нешардированным) PostgreSQL по скорости здесь
запрещено.** Причина не только в том, что ванильный PostgreSQL нигде в
стенде не запускается (хотя это тоже так — сравнивать буквально не с
чем). Дело ещё и в объёме данных: на данных, помещающихся на один узел
(как в этом стенде — тысячи строк), распределённая схема Citus добавляет
слой координации — разбор запроса на задачи, обращения к воркерам, слияние
результатов на координаторе — там, где обычный PostgreSQL просто читает
таблицу напрямую. На ЭТОМ стенде (короткие агрегаты по нескольким тысячам
строк, всё на одном хосте) этот слой — чистые накладные расходы.

**Но общего правила «распределённое всегда проигрывает на малых данных»
отсюда не следует, и стенд такого вопроса не ставит.** Тот же слой даёт и
параллельное исполнение по шардам, которое на CPU-bound запросах иногда
выигрывает даже на объёме, влезающем в один узел. Что перевесит — зависит
от запроса и здесь не измерялось.

Абсолютные миллисекунды артефакта 1 (~12 мс у запроса по ключу против
~55-63 мс у scatter-gather) особенно соблазнительно вставить в фразу «а на
обычном PostgreSQL было бы...» — делать этого нельзя ни в одну, ни в другую
сторону: такое сравнение здесь просто не измерено.

## Ограничение схемы Citus: ключ шардирования обязан быть в PK/UNIQUE/EXCLUDE

Не привязано к одному артефакту — вылезло при самой постановке стенда
(создание `shipments`, `sql/01-schema.sql`) и стоит того, чтобы попасть в
раздел статьи «что смотреть в проде» отдельным пунктом.

Citus не разрешает распределить таблицу, если её `PRIMARY KEY`, `UNIQUE`
или `EXCLUDE`-ограничение не включает колонку шардирования. Дословная
ошибка (`create_distributed_table('shipments', 'order_id')` на исходном
плане `shipments`, где `PRIMARY KEY (shipment_id)` не содержал `order_id`):

```
ERROR:  cannot create constraint on "shipments"
DETAIL:  Distributed relations cannot have UNIQUE, EXCLUDE, or PRIMARY
KEY constraints that do not include the partition column
```

Практическое следствие: если в проде решаете перешардировать существующую
таблицу или добавить `UNIQUE`-ограничение к уже распределённой, это
ограничение проверяется КАЖДЫЙ раз и ничем не обходится — ключ
шардирования должен войти в состав ограничения. В стенде решение —
составной `PRIMARY KEY (shipment_id, order_id)` вместо изначально
задуманного одиночного (см. `sql/01-schema.sql`); в реальной схеме это
может означать пересмотр уникальности на уровне приложения, а не только
СУБД, если исходный уникальный идентификатор не совпадает с ключом
шардирования.

## Артефакт 1: запрос в один шард против scatter-gather

Команда: `bash scripts/scatter-gather-demo.sh`. Один и тот же запрос
(`SELECT count(*) FROM orders WHERE ...`) в двух ветках, отличающихся ровно
одним фильтром: А — `customer_id = 42` (ключ шардирования), Б —
`total > 100` (не ключ шардирования). Каждая ветка прогнана **20 раз** —
Task Count сверяется на всех 20 повторах, а по времени печатаются min /
медиана / p95 / max (не только среднее из пары чисел, как раньше) — чтобы
видеть, насколько устойчиво само число. Сравнивать ФОРМУ ХВОСТА между
ветками по этому замеру нельзя, см. «О разбросе» ниже.

```
[scatter-gather] Preflight: проверяем, что orders распределена по ключу и наполнена…
[scatter-gather] orders: 32 шардов.
[scatter-gather] orders всего: 4000; заказов у customer_id=42: 20.

================================================================
 Ветка А: с фильтром по ключу шардирования (customer_id = 42)
================================================================
                                                               QUERY PLAN
-----------------------------------------------------------------------------------------------------------------------------------------
 Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=0 width=0) (actual time=13.048..13.049 rows=1 loops=1)
   Output: remote_scan.count
   Task Count: 1
   Tuple data received from nodes: 8 bytes
   Tasks Shown: All
   ->  Task
         Query: SELECT count(*) AS count FROM public.orders_102067 orders WHERE (customer_id OPERATOR(pg_catalog.=) 42)
         Tuple data received from node: 8 bytes
         Node: host=citus-w2 port=5432 dbname=shard
         ->  Aggregate  (cost=2.05..2.06 rows=1 width=8) (actual time=0.011..0.012 rows=1 loops=1)
               Output: count(*)
               ->  Seq Scan on public.orders_102067 orders  (cost=0.00..2.00 rows=20 width=0) (actual time=0.005..0.009 rows=20 loops=1)
                     Output: customer_id, order_id, total, created_at
                     Filter: (orders.customer_id = 42)
                     Rows Removed by Filter: 60
             Planning Time: 0.226 ms
             Execution Time: 0.040 ms
 Planning Time: 1.626 ms
 Execution Time: 13.066 ms
(19 rows)
[scatter-gather] Ветка А: 20 повторов, Task Count=1 на всех, Execution Time от 13.066 11.633 12.453 12.244 13.833 13.063 11.934 11.561 12.069 12.009 11.605 11.643 11.417 11.675 11.589 11.994 11.698 11.081 11.051 11.561 мс

================================================================
 Ветка Б: без фильтра по ключу шардирования (total > 100)
================================================================
                                                                   QUERY PLAN
-------------------------------------------------------------------------------------------------------------------------------------------------
 Aggregate  (cost=250.00..250.02 rows=1 width=8) (actual time=60.148..60.149 rows=1 loops=1)
   Output: COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=8) (actual time=60.138..60.141 rows=32 loops=1)
         Output: remote_scan.count
         Task Count: 32
         Tuple data received from nodes: 256 bytes
         Tasks Shown: One of 32
         ->  Task
               Query: SELECT count(*) AS count FROM public.orders_102055 orders WHERE (total OPERATOR(pg_catalog.>) '100'::numeric)
               Tuple data received from node: 8 bytes
               Node: host=citus-w1 port=5432 dbname=shard
               ->  Aggregate  (cost=4.99..5.00 rows=1 width=8) (actual time=0.053..0.054 rows=1 loops=1)
                     Output: count(*)
                     ->  Seq Scan on public.orders_102055 orders  (cost=0.00..4.50 rows=196 width=0) (actual time=0.013..0.044 rows=196 loops=1)
                           Output: customer_id, order_id, total, created_at
                           Filter: (orders.total > '100'::numeric)
                           Rows Removed by Filter: 4
                   Planning Time: 0.158 ms
                   Execution Time: 0.064 ms
 Planning Time: 1.929 ms
 Execution Time: 60.197 ms
(21 rows)
[scatter-gather] Ветка Б: 20 повторов, Task Count=32 на всех, Execution Time от 60.197 55.495 55.812 60.503 56.008 72.744 54.973 55.978 54.996 54.447 59.427 63.932 57.266 58.115 55.293 60.907 56.434 62.485 55.259 56.021 мс

================================================================
 Сводка
================================================================
Запрос                                  Task Count   min мс   медиана p95        max мс
А: customer_id = 42 (по ключу)        1            11.051     11.675     13.066     13.833
Б: total > 100 (не по ключу)        32           54.447     56.021     63.932     72.744

(20 повторов на ветку. Task Count — СТРУКТУРНАЯ величина и остаётся
главной; перцентили времени — дополнение, не замена, см. оговорку об
однохостовом стенде ниже.)

================================================================
 Падающий вариант (что было бы, если бы маршрутизация не работала)
================================================================
Если бы координатор не умел исключать шарды по значению ключа
шардирования, ОБА запроса опрашивали бы все 32 шардов, и
Task Count у А и Б совпадал бы (оба = 32). Именно это
число и есть провал демонстрации — оно проверяется ниже.

================================================================
 Самопроверка
================================================================
[OK] Task Count воспроизводится точно на всех 20 повторах каждой ветки: А=1, Б=32. Маршрутизация по ключу шардирования подтверждена структурно.

Время — только порядок величины: А min/медиана/p95/max = 11.051/11.675/13.066/13.833 мс,
Б min/медиана/p95/max = 54.447/56.021/63.932/72.744 мс.
Это однохостовый стенд: все узлы на localhost, сети между ними фактически нет.
На настоящем кластере добавятся сетевой объём и удалённые взаимодействия.
ВЕЛИЧИНА этого эффекта здесь НЕ ИЗМЕРЕНА, и НАПРАВЛЕНИЕ изменения разрыва Б/А
не утверждается. Прикидка «число строк × RTT» неверна: строки идут потоком,
а не по одному round-trip на строку.

ПРО ВЫВОД ПЛАНА Б: строка «Tasks Shown: One of 32» — это штатное
поведение Citus, а не усечённый или сбойный вывод. EXPLAIN печатает подробности
ОДНОЙ задачи из 32 в качестве образца, потому что остальные устроены так же.
Сколько задач было на самом деле, видно в строке «Task Count» выше — именно она
здесь и является измеряемой величиной.
[scatter-gather] Готово.
```

Код выхода: `0`.

**Разбор.** Координатор умеет исключать шарды по значению колонки
шардирования (`customer_id`): запрос А трогает ровно один шард (`Task
Count: 1`), потому что `customer_id = 42` однозначно указывает на один
шард по хеш-функции. Запрос Б фильтрует по `total` — колонке, не
участвующей в шардировании, — поэтому координатор не может исключить ни
один шард заранее и опрашивает все 32 (`Task Count: 32`).

**Падающий вариант.** Если бы координатор не умел исключать шарды по
значению ключа шардирования, оба запроса опрашивали бы все 32 шарда и
`Task Count` у А и Б совпадал бы. Вместо этого А=1, Б=32 — воспроизведено
точно на всех 20 повторах каждой ветки.

**О разбросе: вывод о форме хвоста СНЯТ.** Ранняя редакция этого файла (и
статьи) утверждала, что относительный хвост тяжелее у ветки Б, и объясняла
это веером задач (fan-out): 32 задачи против одной. Утверждение снято по
двум независимым причинам.

*Причина 1: не воспроизвелось.* Повторный прогон дал ОБРАТНУЮ картину —
относительный хвост оказался тяжелее у ветки с ОДНОЙ задачей. Числа обоих
прогонов, как есть (min / медиана / p95 / max, мс):

| Прогон | `Task Count 1` (ветка А) | `Task Count 32` (ветка Б) |
|---|---|---|
| Первый (машина автора) | 11.218 / 11.874 / 12.975 / 13.341 | 55.036 / 56.510 / 63.728 / 67.366 |
| Повторный (снят внешним ревью, не на этой машине) | 11.655 / 12.695 / 24.937 / 30.308 | 58.296 / 61.944 / 76.842 / 80.618 |
| Третий (перепроверка после правки формулировок, машина автора) | 10.958 / 11.741 / 12.136 / 13.022 | 54.914 / 56.263 / 59.447 / 62.566 |
| Четвёртый (**канонический вывод выше**, машина автора) | 11.051 / 11.675 / 13.066 / 13.833 | 54.447 / 56.021 / 63.932 / 72.744 |

⚠️ Происхождение второй строки указано намеренно: остальные числа этого
файла сняты на машине автора, а эти — сторонним прогоном при ревью. Для
вывода «не воспроизводится» это не помеха, скорее наоборот. Но выдавать их
за собственный замер нельзя.

В первом прогоне max/медиана у А = 1.12, у Б = 1.19; в повторном у А = 2.39,
у Б = 1.30 — знак разницы поменялся. Значит первое наблюдение было
свойством конкретного прогона, а не свойством веера. Третий прогон даёт
max/медиана 1.11 у ОБЕИХ веток — разницы нет вовсе. Четвёртый (тот, что
записан выше) даёт 1.18 у А и 1.30 у Б, то есть снова «тяжелее у Б», как в
первом. **Четыре прогона дали три разные картины — Б тяжелее, А тяжелее,
поровну — и это ровно тот результат, ради которого таблица здесь и стоит:**
величина от прогона к прогону плавает и выводов про хвост не несёт. Тот
факт, что канонический прогон случайно согласуется с исходным (снятым)
утверждением, ничего не восстанавливает — при таком разбросе совпадение
знака в отдельном прогоне не является свидетельством.

*Причина 2: эксперимент негоден для такого вывода в принципе.* Даже если бы
картина воспроизводилась, этот замер её не объяснил бы: ветки отличаются не
только числом задач, но и предикатом (`customer_id = 42` против
`total > 100`), и суммарным объёмом сканирования. Влияние веера здесь не
изолировано ни от чего. Форму хвоста нужно проверять отдельным опытом, где
меняется ТОЛЬКО число задач.

**Поэтому FIXTURES не утверждает ничего ни о форме хвоста, ни о причине
разброса.** Перцентили печатаются и записаны как есть — чтобы видеть, что
время в целом устойчиво по порядку величины, — и не более того. Отдельно:
первый отсчёт серии раньше называли здесь выбросом с холодным кешем; эта
атрибуция тоже убрана — она была объяснением, а не замером, и в
каноническом прогоне первый отсчёт лежит ВНУТРИ основного диапазона своей
ветки.

**Что воспроизводится точно — `Task Count`: 1 против 32**, на всех 20
повторах каждой ветки, в обоих прогонах. Это и есть результат артефакта.
**`Task Count: 32` — это 32 задачи по шардам, а не 32 узла:** воркеров на
стенде ДВА, и 32 задачи раскладываются по ним. Смешивать задачи, шарды и
физические узлы нельзя — величины разные. Раскладка «по 16 на воркер» взята
не из `EXPLAIN` (он числа задач по узлам не разбивает), а из размещения
шардов: `pg_dist_placement` даёт по 16 шардов `orders` на каждом воркере
(см. снимок артефакта 5: 49 = 3 × 16 + 1 копия референсной `carriers`).
Задача идёт туда, где лежит её шард, поэтому 16/16 — следствие раскладки
размещений, а не отдельно снятая величина.

Величина выборки (20) достаточна, чтобы отличить устойчивое число от шума
одного замера, но недостаточна для точной оценки «настоящего» p95
(доверительный интервал у перцентиля по 20 наблюдениям широк) — это
порядок величины, как и остальное время в этом файле.

**⚠️ Обязательные оговорки.**

1. Время — только порядок величины: min/медиана/p95/max = 11.1/11.7/13.1/13.8 мс
   у А против 54.4/56.0/63.9/72.7 мс у Б в этом прогоне. Это однохостовый
   стенд: все узлы на localhost, сети между ними фактически нет. На
   настоящем кластере добавятся сетевой объём и удалённые взаимодействия.
   ВЕЛИЧИНА ЭТОГО ЭФФЕКТА ЗДЕСЬ НЕ ИЗМЕРЕНА, и направление изменения
   разрыва А/Б не утверждается. Прикидка «число строк × RTT» неверна:
   строки идут потоком, а не по одному round-trip на строку. Саму разницу
   в `Task Count` (1 против 32) сеть не отменяет — она структурная. Про
   форму хвоста этот замер не говорит ничего, см. «О разбросе» выше.
2. `Tasks Shown: One of 32` в плане Б — не усечённый вывод, см. пояснение
   в самом выводе скрипта выше.
3. **Сравнение с обычным (нешардированным) PostgreSQL по скорости здесь
   запрещено** — см. глобальную оговорку в начале файла. Этот артефакт —
   самое соблазнительное место её нарушить: 11-12 мс у А выглядят «почти
   как обычный PostgreSQL», но ванильный PostgreSQL здесь не запускался и
   не измерялся. На этом игрушечном объёме (4000 строк) слой координации
   Citus — чистые накладные расходы, однако в общее правило это не
   разворачивается: см. вторую главную оговорку в начале файла.

## Артефакт 2: колокация против репартиционного join

Команда: `bash scripts/colocation-demo.sh`. Два join одинаковой формы
(join + `GROUP BY` + `count(*)`), отличающиеся ровно одной переменной —
колоцированы ли соединяемые таблицы. А — `orders JOIN customers USING
(customer_id)`, обе таблицы в одной группе колокации (`colocationid=1`).
Б — `orders JOIN shipments USING (order_id)`, `shipments` — в отдельной
группе (`colocationid=2`, ключ другой). Ветка Б показана в двух
состояниях: по умолчанию (`citus.enable_repartition_joins=off`) и с
`SET citus.enable_repartition_joins = on` на уровне одной сессии.

```
[colocation] Preflight: проверяем распределение и колокацию customers/orders/shipments…
[colocation] customers/orders: colocationid=1 (общая группа). shipments: colocationid=2 (отдельная группа).
[colocation] orders=4000 customers=200 shipments=4000. citus.enable_repartition_joins по умолчанию: off.

================================================================
 Прогон 1 / 2
================================================================

--- А: orders JOIN customers USING (customer_id) — колоцированы ---
                                                                                               QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=69.587..69.589 rows=5 loops=1)
   Output: remote_scan.city, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.city
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=69.549..69.557 rows=115 loops=1)
         Output: remote_scan.city, remote_scan.count
         Task Count: 32
         Tuple data received from nodes: 1775 bytes
         Tasks Shown: One of 32
         ->  Task
               Query: SELECT c.city, count(*) AS count FROM (public.orders_102041 o JOIN public.customers_102009 c ON ((o.customer_id OPERATOR(pg_catalog.=) c.customer_id))) WHERE true GROUP BY c.city
               Tuple data received from node: 57 bytes
               Node: host=citus-w2 port=5432 dbname=shard
               ->  HashAggregate  (cost=9.74..10.94 rows=120 width=40) (actual time=0.562..0.564 rows=4 loops=1)
                     Output: c.city, count(*)
                     Group Key: c.city
                     Batches: 1  Memory Usage: 40kB
                     ->  Nested Loop  (cost=0.16..9.14 rows=120 width=32) (actual time=0.374..0.443 rows=120 loops=1)
                           Output: c.city
                           Inner Unique: true
                           ->  Seq Scan on public.orders_102041 o  (cost=0.00..2.20 rows=120 width=8) (actual time=0.014..0.024 rows=120 loops=1)
                                 Output: o.customer_id, o.order_id, o.total, o.created_at
                           ->  Memoize  (cost=0.16..0.58 rows=1 width=40) (actual time=0.003..0.003 rows=1 loops=120)
                                 Output: c.city, c.customer_id
                                 Cache Key: o.customer_id
                                 Cache Mode: logical
                                 Hits: 114  Misses: 6  Evictions: 0  Overflows: 0  Memory Usage: 1kB
                                 ->  Index Scan using customers_pkey_102009 on public.customers_102009 c  (cost=0.15..0.57 rows=1 width=40) (actual time=0.029..0.029 rows=1 loops=6)
                                       Output: c.city, c.customer_id
                                       Index Cond: (c.customer_id = o.customer_id)
                   Planning Time: 0.934 ms
                   Execution Time: 0.726 ms
 Planning Time: 3.468 ms
 Execution Time: 69.722 ms
(34 rows)
[colocation] Прогон 1, А: Task Count=32, MapMergeJob в плане=0, Execution Time=69.722 мс

--- Б (по умолчанию, citus.enable_repartition_joins=off): orders JOIN shipments USING (order_id) — НЕ колоцированы ---
ERROR:  the query contains a join that requires repartitioning
HINT:  Set citus.enable_repartition_joins to on to enable repartitioning
[colocation] Прогон 1, Б (по умолчанию): код возврата psql=1

--- Б (citus.enable_repartition_joins=on на уровне сессии): та же пара таблиц ---
SET
                                                        QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=865.877..865.879 rows=5 loops=1)
   Output: remote_scan.carrier, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.carrier
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=865.856..865.860 rows=40 loops=1)
         Output: remote_scan.carrier, remote_scan.count
         Task Count: 8
         Tuple data received from nodes: 472 bytes
         Tasks Shown: None, not supported for re-partition queries
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
 Planning Time: 5.100 ms
 Execution Time: 866.067 ms
(17 rows)
[colocation] Прогон 1, Б (репартиция включена): Task Count=8, MapMergeJob в плане=2, Execution Time=866.067 мс

================================================================
 Прогон 2 / 2
================================================================

--- А: orders JOIN customers USING (customer_id) — колоцированы ---
                                                                                               QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=58.056..58.058 rows=5 loops=1)
   Output: remote_scan.city, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.city
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=58.022..58.028 rows=115 loops=1)
         Output: remote_scan.city, remote_scan.count
         Task Count: 32
         Tuple data received from nodes: 1775 bytes
         Tasks Shown: One of 32
         ->  Task
               Query: SELECT c.city, count(*) AS count FROM (public.orders_102054 o JOIN public.customers_102022 c ON ((o.customer_id OPERATOR(pg_catalog.=) c.customer_id))) WHERE true GROUP BY c.city
               Tuple data received from node: 66 bytes
               Node: host=citus-w2 port=5432 dbname=shard
               ->  HashAggregate  (cost=14.46..16.46 rows=200 width=40) (actual time=0.135..0.137 rows=4 loops=1)
                     Output: c.city, count(*)
                     Group Key: c.city
                     Batches: 1  Memory Usage: 40kB
                     ->  Nested Loop  (cost=0.16..13.46 rows=200 width=32) (actual time=0.027..0.103 rows=200 loops=1)
                           Output: c.city
                           Inner Unique: true
                           ->  Seq Scan on public.orders_102054 o  (cost=0.00..4.00 rows=200 width=8) (actual time=0.010..0.025 rows=200 loops=1)
                                 Output: o.customer_id, o.order_id, o.total, o.created_at
                           ->  Memoize  (cost=0.16..0.42 rows=1 width=40) (actual time=0.000..0.000 rows=1 loops=200)
                                 Output: c.city, c.customer_id
                                 Cache Key: o.customer_id
                                 Cache Mode: logical
                                 Hits: 190  Misses: 10  Evictions: 0  Overflows: 0  Memory Usage: 2kB
                                 ->  Index Scan using customers_pkey_102022 on public.customers_102022 c  (cost=0.15..0.41 rows=1 width=40) (actual time=0.002..0.002 rows=1 loops=10)
                                       Output: c.city, c.customer_id
                                       Index Cond: (c.customer_id = o.customer_id)
                   Planning Time: 0.411 ms
                   Execution Time: 0.198 ms
 Planning Time: 2.785 ms
 Execution Time: 58.141 ms
(34 rows)
[colocation] Прогон 2, А: Task Count=32, MapMergeJob в плане=0, Execution Time=58.141 мс

--- Б (по умолчанию, citus.enable_repartition_joins=off): orders JOIN shipments USING (order_id) — НЕ колоцированы ---
ERROR:  the query contains a join that requires repartitioning
HINT:  Set citus.enable_repartition_joins to on to enable repartitioning
[colocation] Прогон 2, Б (по умолчанию): код возврата psql=1

--- Б (citus.enable_repartition_joins=on на уровне сессии): та же пара таблиц ---
SET
                                                        QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=873.540..873.542 rows=5 loops=1)
   Output: remote_scan.carrier, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.carrier
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=873.521..873.523 rows=40 loops=1)
         Output: remote_scan.carrier, remote_scan.count
         Task Count: 8
         Tuple data received from nodes: 472 bytes
         Tasks Shown: None, not supported for re-partition queries
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
 Planning Time: 6.073 ms
 Execution Time: 873.787 ms
(17 rows)
[colocation] Прогон 2, Б (репартиция включена): Task Count=8, MapMergeJob в плане=2, Execution Time=873.787 мс

================================================================
 Сводка
================================================================
Прогон Запрос                                         Task Count   MapMergeJob      Execution ms
1          А: orders+customers (колоцированы)      32           0                69.722
1          Б: orders+shipments (репартиция, on)      8            2                866.067
2          А: orders+customers (колоцированы)      32           0                58.141
2          Б: orders+shipments (репартиция, on)      8            2                873.787

Б по умолчанию (citus.enable_repartition_joins=off) в обоих прогонах
отказалась выполняться — см. дословный текст ошибки выше и в проверке ниже.

================================================================
 Падающий вариант (что было бы, если бы колокация не влияла)
================================================================
Если бы Citus мог выполнить join А и join Б одинаково — без разницы,
колоцированы таблицы или нет — оба плана выглядели бы одинаково: либо
ни в одном из них не было бы узла MapMergeJob (репартиции никогда не
требуется), либо он был бы в обоих. То, что MapMergeJob присутствует
СТРОГО у Б и отсутствует у А — и есть демонстрируемое отличие; именно
это и проверяется ниже.

================================================================
 Самопроверка
================================================================
[OK] Структурные величины воспроизводятся точно между прогонами:
     А (колоцированы): Task Count=32, MapMergeJob=0 — join выполняется локально на каждом шарде.
     Б по умолчанию: отказ планировщика, дословно: «ERROR:  the query contains a join that requires repartitioning».
     Б с включённой репартицией: Task Count=8, MapMergeJob>0 — join требует переброски данных между воркерами.

Время — только порядок величины. Все узлы на одном хосте; на реальном
кластере добавятся сетевой объём и удалённые взаимодействия, но их
величина здесь НЕ измерена, и направление изменения разрыва Б/А
утверждать нельзя:
  А ~ 69.722/58.141 мс, Б (репартиция) ~ 866.067/873.787 мс.

citus.enable_repartition_joins включался ИСКЛЮЧИТЕЛЬНО через SET внутри
одного psql-подключения (одна простая query-message: SET + EXPLAIN).
Каждый вызов psql в этом скрипте — новое подключение, поэтому изменение
не переживает вызов и кластер не остаётся в изменённом состоянии для
следующих артефактов.
[colocation] Готово.
```

Код выхода: `0`.

**Разбор.** `orders` и `customers` лежат в одной группе колокации —
парные шарды по `customer_id` физически совпадают на одном узле, поэтому
join выполняется целиком внутри одной задачи (`Task`), `MapMergeJob` в
плане нет. `shipments` шардирована по `order_id`, в отдельной группе
колокации (см. раздел «Артефакт 1» брифа Task 1 — по документации Citus без
явного `colocate_with => 'none'` она попала бы в общую группу с
`customers`/`orders` при совпадении типа и числа шардов; **стенд этот
контрфактический вариант не запускал** — `shipments` создаётся сразу с
`colocate_with => 'none'`, так что здесь это заявление документации, а не
измерение). По
умолчанию Citus вообще отказывается строить план такого join
(`citus.enable_repartition_joins` выключен) — с включённым параметром
план появляется, и в нём дважды присутствует `MapMergeJob` (по разу на
каждую перераспределяемую таблицу): `Map Task Count: 32` (по числу
шардов), `Merge Task Count: 8` — именно 8 и есть верхний `Task Count`
плана.

**Падающий вариант.** Если бы Citus выполнял join с колокацией и без неё
одинаково, оба плана содержали бы `MapMergeJob` либо ни один. Вместо этого
он строго присутствует у Б и строго отсутствует у А.

**⚠️ Обязательные оговорки.**

1. Время — порядок величины: А ~58.1–69.7 мс, Б (репартиция) ~866.1–873.8 мс
   в этом прогоне. Это однохостовый стенд: все узлы на localhost, сети между
   ними фактически нет. На настоящем кластере добавятся сетевой объём
   (переброска данных между узлами) и удалённые взаимодействия. ВЕЛИЧИНА
   ЭТОГО ЭФФЕКТА ЗДЕСЬ НЕ ИЗМЕРЕНА, и направление изменения разрыва Б/А не
   утверждается — сетевые издержки ложатся на обе ветки, и как сдвинется их
   отношение, из этого прогона не следует. Прикидка «число строк × RTT»
   тоже неверна: строки идут потоком, а не по одному round-trip на строку.
2. **`Task Count: 8` у репартиционного join меньше, чем `Task Count: 32` у
   локального (А), и это НЕ признак эффективности.** Величины структурно
   про разное: у А — число шардов, у Б — число merge-задач финальной стадии
   репартиции (`Merge Task Count`). Откуда конкретно берётся число 8, в
   рамках этого стенда не выяснялось — записано как наблюдаемый факт.
3. Сравнение Citus с обычным (нешардированным) PostgreSQL по скорости
   этот артефакт не проводит — ванильный PostgreSQL здесь не запускается
   вовсе.

## Артефакт 3: референсная таблица против распределённой копии справочника

Команда: `bash scripts/reference-demo.sh`. Join `shipments` со справочником
перевозчиков в двух ролях, отличающихся ровно одной переменной — способом
распределения справочника. А — `carriers`, референсная таблица
(копия целиком на каждом воркере; координатор — тоже узел кластера, но
размещений референсной таблицы у него нет). Б — `carriers_sharded`, те же 5 строк,
распределённые по ключу `carrier`; таблицу создаёт и удаляет сам скрипт.
Как и в артефакте 2, ветка Б показана в двух состояниях
(`citus.enable_repartition_joins` off/on).

```
[reference] Preflight: проверяем, что carriers — референсная таблица, а shipments распределена и наполнена…
[reference] Активных воркеров в pg_dist_node: 2 (число размещений референсной таблицы ожидается равным этому числу).
[reference] carriers: 5 строк (референсная). shipments: 4000 строк, colocationid=2. citus.enable_repartition_joins по умолчанию: off.
[reference] Preflight: если carriers_sharded осталась от прерванного прогона — удаляем перед началом…
NOTICE:  table "carriers_sharded" does not exist, skipping
[reference] Создаём carriers_sharded — распределённую копию carriers по ключу carrier…
CREATE TABLE
INSERT 0 5
NOTICE:  Copying data from local table...
NOTICE:  copying the data has completed
DETAIL:  The local data in the table is no longer visible, but is still on disk.
HINT:  To remove the local data, run: SELECT truncate_local_data_after_distributing_table($$public.carriers_sharded$$)
[reference] carriers_sharded создана: 5 строк, colocationid=20 (отдельная группа от shipments).
[reference] Размещение А (carriers, референсная):    шардов=1, размещений=2 (воркеров в кластере: 2).
[reference] Размещение Б (carriers_sharded, распр.): шардов=32, размещений=32.

================================================================
 Прогон 1 / 2
================================================================

--- А: shipments JOIN carriers (референсная) ---
                                                                                               QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=69.347..69.349 rows=3 loops=1)
   Output: remote_scan.country, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.country
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=69.308..69.316 rows=96 loops=1)
         Output: remote_scan.country, remote_scan.count
         Task Count: 32
         Tuple data received from nodes: 960 bytes
         Tasks Shown: One of 32
         ->  Task
               Query: SELECT c.country, count(*) AS count FROM (public.shipments_102073 s JOIN public.carriers_102104 c ON ((s.carrier OPERATOR(pg_catalog.=) c.carrier))) WHERE true GROUP BY c.country
               Tuple data received from node: 30 bytes
               Node: host=citus-w2 port=5432 dbname=shard
               ->  HashAggregate  (cost=9.49..10.83 rows=134 width=40) (actual time=0.501..0.503 rows=3 loops=1)
                     Output: c.country, count(*)
                     Group Key: c.country
                     Batches: 1  Memory Usage: 40kB
                     ->  Nested Loop  (cost=0.16..8.82 rows=134 width=32) (actual time=0.396..0.468 rows=134 loops=1)
                           Output: c.country
                           Inner Unique: true
                           ->  Seq Scan on public.shipments_102073 s  (cost=0.00..2.34 rows=134 width=4) (actual time=0.016..0.024 rows=134 loops=1)
                                 Output: s.shipment_id, s.order_id, s.carrier
                           ->  Memoize  (cost=0.16..0.54 rows=1 width=64) (actual time=0.003..0.003 rows=1 loops=134)
                                 Output: c.country, c.carrier
                                 Cache Key: s.carrier
                                 Cache Mode: logical
                                 Hits: 129  Misses: 5  Evictions: 0  Overflows: 0  Memory Usage: 1kB
                                 ->  Index Scan using carriers_pkey_102104 on public.carriers_102104 c  (cost=0.15..0.53 rows=1 width=64) (actual time=0.077..0.077 rows=1 loops=5)
                                       Output: c.country, c.carrier
                                       Index Cond: (c.carrier = s.carrier)
                   Planning Time: 0.645 ms
                   Execution Time: 0.591 ms
 Planning Time: 2.424 ms
 Execution Time: 69.455 ms
(34 rows)
[reference] Прогон 1, А: Task Count=32, MapMergeJob в плане=0, Execution Time=69.455 мс

--- Б (по умолчанию, citus.enable_repartition_joins=off): shipments JOIN carriers_sharded — распределённая копия ---
ERROR:  the query contains a join that requires repartitioning
HINT:  Set citus.enable_repartition_joins to on to enable repartitioning
[reference] Прогон 1, Б (по умолчанию): код возврата psql=1

--- Б (citus.enable_repartition_joins=on на уровне сессии): та же пара таблиц ---
SET
                                                         QUERY PLAN
----------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=1566.639..1566.642 rows=3 loops=1)
   Output: remote_scan.country, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.country
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=1566.615..1566.617 rows=5 loops=1)
         Output: remote_scan.country, remote_scan.count
         Task Count: 8
         Tuple data received from nodes: 50 bytes
         Tasks Shown: None, not supported for re-partition queries
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
 Planning Time: 6.391 ms
 Execution Time: 1566.967 ms
(17 rows)
[reference] Прогон 1, Б (репартиция включена): Task Count=8 (Map Task Count=32, Merge Task Count=8), MapMergeJob в плане=2, Execution Time=1566.967 мс

================================================================
 Прогон 2 / 2
================================================================

--- А: shipments JOIN carriers (референсная) ---
                                                                                               QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=127.089..127.093 rows=3 loops=1)
   Output: remote_scan.country, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.country
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=127.035..127.045 rows=96 loops=1)
         Output: remote_scan.country, remote_scan.count
         Task Count: 32
         Tuple data received from nodes: 960 bytes
         Tasks Shown: One of 32
         ->  Task
               Query: SELECT c.country, count(*) AS count FROM (public.shipments_102098 s JOIN public.carriers_102104 c ON ((s.carrier OPERATOR(pg_catalog.=) c.carrier))) WHERE true GROUP BY c.country
               Tuple data received from node: 30 bytes
               Node: host=citus-w1 port=5432 dbname=shard
               ->  HashAggregate  (cost=9.28..10.53 rows=125 width=40) (actual time=0.259..0.261 rows=3 loops=1)
                     Output: c.country, count(*)
                     Group Key: c.country
                     Batches: 1  Memory Usage: 40kB
                     ->  Nested Loop  (cost=0.16..8.66 rows=125 width=32) (actual time=0.023..0.235 rows=125 loops=1)
                           Output: c.country
                           Inner Unique: true
                           ->  Seq Scan on public.shipments_102098 s  (cost=0.00..2.25 rows=125 width=4) (actual time=0.012..0.020 rows=125 loops=1)
                                 Output: s.shipment_id, s.order_id, s.carrier
                           ->  Memoize  (cost=0.16..0.56 rows=1 width=64) (actual time=0.002..0.002 rows=1 loops=125)
                                 Output: c.country, c.carrier
                                 Cache Key: s.carrier
                                 Cache Mode: logical
                                 Hits: 120  Misses: 5  Evictions: 0  Overflows: 0  Memory Usage: 1kB
                                 ->  Index Scan using carriers_pkey_102104 on public.carriers_102104 c  (cost=0.15..0.55 rows=1 width=64) (actual time=0.002..0.002 rows=1 loops=5)
                                       Output: c.country, c.carrier
                                       Index Cond: (c.carrier = s.carrier)
                   Planning Time: 0.138 ms
                   Execution Time: 0.298 ms
 Planning Time: 3.247 ms
 Execution Time: 127.368 ms
(34 rows)
[reference] Прогон 2, А: Task Count=32, MapMergeJob в плане=0, Execution Time=127.368 мс

--- Б (по умолчанию, citus.enable_repartition_joins=off): shipments JOIN carriers_sharded — распределённая копия ---
ERROR:  the query contains a join that requires repartitioning
HINT:  Set citus.enable_repartition_joins to on to enable repartitioning
[reference] Прогон 2, Б (по умолчанию): код возврата psql=1

--- Б (citus.enable_repartition_joins=on на уровне сессии): та же пара таблиц ---
SET
                                                         QUERY PLAN
----------------------------------------------------------------------------------------------------------------------------
 HashAggregate  (cost=500.00..503.50 rows=200 width=40) (actual time=1763.182..1763.185 rows=3 loops=1)
   Output: remote_scan.country, COALESCE((pg_catalog.sum(remote_scan.count))::bigint, '0'::bigint)
   Group Key: remote_scan.country
   Batches: 1  Memory Usage: 40kB
   ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=40) (actual time=1763.160..1763.162 rows=5 loops=1)
         Output: remote_scan.country, remote_scan.count
         Task Count: 8
         Tuple data received from nodes: 50 bytes
         Tasks Shown: None, not supported for re-partition queries
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
         ->  MapMergeJob
               Map Task Count: 32
               Merge Task Count: 8
 Planning Time: 13.675 ms
 Execution Time: 1763.541 ms
(17 rows)
[reference] Прогон 2, Б (репартиция включена): Task Count=8 (Map Task Count=32, Merge Task Count=8), MapMergeJob в плане=2, Execution Time=1763.541 мс

================================================================
 Сводка
================================================================
Прогон Запрос                                               Task Count   MapMergeJob      Execution ms
1          А: shipments+carriers (референсная)            32           0                69.455
1          Б: shipments+carriers_sharded (репартиция, on)  8            2                1566.967
2          А: shipments+carriers (референсная)            32           0                127.368
2          Б: shipments+carriers_sharded (репартиция, on)  8            2                1763.541

Б по умолчанию (citus.enable_repartition_joins=off) в обоих прогонах
отказалась выполняться — см. дословный текст ошибки выше и в проверке ниже.

ПРО Task Count=8 У ВЕТКИ Б. Число меньше 32 шардов не потому, что
запрос прочитал меньше данных: верхний Task Count репартиционного плана —
это счётчик ПОСЛЕДНЕЙ (merge) стадии, Merge Task Count=8. Шарды
сканируются на первой стадии, и там счётчик другой: Map Task Count=32
— ровно по числу шардов shipments. Обе строки видны внутри узлов
MapMergeJob в плане выше.

================================================================
 Физическое размещение справочника (чем это отличается от артефакта 2)
================================================================
Таблица                                 Шардов Размещений
А: carriers (референсная)          1          2
Б: carriers_sharded (распределённая) 32         32

Воркеров в кластере (pg_dist_node, динамически): 2.

Вот собственный признак референсной таблицы, которого у колокации НЕТ.
У А один-единственный шард, но размещений у него 2 — по одному на
каждый ВОРКЕР: это ОДНА И ТА ЖЕ копия справочника, продублированная на все
ВОРКЕРЫ. Координатор — тоже узел кластера, но размещений референсной таблицы
на нём нет, поэтому «на всех узлах» было бы неверно.
Поэтому join с ней локален с ЧЕМ УГОДНО — копия уже физически лежит
рядом с любыми данными, независимо от того, по какому ключу те разложены.
У Б 32 шардов и ровно 32 размещений — по одному на шард, репликации
нет, каждый кусок справочника лежит на одном узле, и join с ним требует
переброски.

Артефакт 2 (колокация) добивался локальности ДРУГИМ способом: там у обеих
таблиц по 32 шарда и по одному размещению на шард, а join локален потому,
что парные шарды согласованно разложены по общему ключу и лежат на одном
узле. Репликации там нет вовсе. По виду плана эти два механизма
неразличимы (в обоих случаях MapMergeJob=0) — различает их именно
таблица размещений выше.

================================================================
 Падающий вариант (что было бы, если бы референсность не влияла)
================================================================
Если бы Citus выполнял join с референсной и с распределённой копией
справочника одинаково — без разницы, лежит ли копия на каждом воркере или
справочник распределён по своему ключу — оба плана выглядели бы
одинаково: либо ни в одном из них не было бы узла MapMergeJob
(переброска данных никогда не требуется), либо он был бы в обоих. То,
что MapMergeJob присутствует СТРОГО у Б (репартиция) и отсутствует
СТРОГО у А (референсная) — и есть демонстрируемое отличие; именно это
и проверяется ниже.

================================================================
 Самопроверка
================================================================
[reference] Удаляем carriers_sharded явно (до завершения скрипта, не дожидаясь trap)…
[OK] carriers_sharded удалена, схема стенда вернулась к исходной: 4 распределённые/референсные таблицы (customers, orders, shipments, carriers), лишних объектов нет.

[OK] Структурные величины воспроизводятся точно между прогонами:
     А (референсная carriers): Task Count=32, MapMergeJob=0 — join выполняется локально на каждой задаче shipments, копия справочника уже на месте.
     Б по умолчанию (carriers_sharded): отказ планировщика, дословно: «ERROR:  the query contains a join that requires repartitioning».
     Б с включённой репартицией: Task Count=8 (Map=32, Merge=8), MapMergeJob=2 — join с распределённой копией того же справочника требует переброски данных между воркерами.

[OK] Физическое размещение подтверждает, ПОЧЕМУ join А локален:
     А (carriers, референсная):    шардов=1, размещений=2 = число воркеров (2) — один шард, скопированный на каждый ВОРКЕР (координатор — тоже узел кластера, но размещения у него нет).
     Б (carriers_sharded, распр.): шардов=32, размещений=32 — по одному размещению на шард, репликации нет.
     Локальность join у А объясняется РЕПЛИКАЦИЕЙ справочника, а не колокацией:
     это то новое, чего не показывает артефакт 2, где локальность достигалась
     согласованным разложением по общему ключу без единой лишней копии.

Время — только порядок величины. Все узлы на одном хосте; на реальном
кластере добавятся сетевой объём и удалённые взаимодействия, но их
величина здесь НЕ измерена, и направление изменения разрыва Б/А
утверждать нельзя:
  А ~ 69.455/127.368 мс, Б (репартиция) ~ 1566.967/1763.541 мс.
Сравнение Citus с обычным PostgreSQL по скорости в этом артефакте не
проводится — ванильный PostgreSQL здесь не запускается вовсе.

citus.enable_repartition_joins включался ИСКЛЮЧИТЕЛЬНО через SET внутри
одного psql-подключения (одна простая query-message: SET + EXPLAIN).
Каждый вызов psql в этом скрипте — новое подключение, поэтому изменение
не переживает вызов и кластер не остаётся в изменённом состоянии для
следующих артефактов.
[reference] Готово.
[reference] Уборка: удаляем carriers_sharded (существует только для этой демонстрации)…
```

Код выхода: `0`.

**Разбор.** Референсная таблица (`carriers`) — это один «шард», но с
размещением НА КАЖДОМ ВОРКЕРЕ (`shard_count=1`, `placement_count = число
воркеров`; в этом прогоне — 1 шард и 2 размещения на двух воркерах).
Координатор — тоже узел кластера, но размещений референсной таблицы на нём
НЕТ, поэтому «на каждом узле» было бы неверно. В ЭТОМ артефакте join с ней
выполнился локально — `MapMergeJob` в плане нет; копия справочника лежит на
том же воркере, что и любой шард `shipments`, поэтому перебрасывать для
соединения было нечего. **«Join с референсной таблицей локален ВСЕГДА» отсюда
не следует:** измерен ровно один запрос одной формы (`shipments JOIN
carriers`). Запрос, где помимо референсной соединяются две по-разному
распределённые таблицы, репартиции всё равно потребует — такой случай здесь
не ставился.
Это другой механизм локальности, чем колокация из артефакта 2: там оба
участника join разложены по 32 шарда с одним размещением на шард каждый, а
локальность возникает из согласованного разбиения по общему ключу, без
единой лишней копии. По тексту `EXPLAIN` (`MapMergeJob` есть/нет) эти два
механизма неразличимы — различает их только таблица размещений
(`pg_dist_shard`/`pg_dist_placement`).

**Падающий вариант.** Если бы Citus выполнял join с референсной и с
распределённой копией одинаково, `MapMergeJob` был бы в обоих планах или
ни в одном. Вместо этого он строго есть у Б и строго отсутствует у А —
именно это и проверяет самопроверка.

**⚠️ Обязательные оговорки.**

1. Время — порядок величины: А ~69.5–127.4 мс, Б (репартиция) ~1567.0–1763.5 мс
   в этом прогоне. Разброс у А между двумя прогонами почти двукратный
   (69.455 против 127.368 мс) — лишний повод не цитировать абсолютные
   миллисекунды. Та же оговорка про однохостовость, что и в артефакте 2.
2. `Task Count: 8` у Б — это `Merge Task Count` финальной стадии
   репартиции, а не число прочитанных шардов (их 32, видно как `Map Task
   Count` внутри `MapMergeJob`). Меньшее число не означает меньший объём
   работы.
3. Сравнение с обычным PostgreSQL по скорости не проводится — см. также
   глобальную оговорку в начале файла (проигрыш распределённой схемы на
   этом объёме данных не специфика этого артефакта, а общее свойство).
4. **`colocationid=20` у `carriers_sharded` в сыром выводе выше — не
   цитируемое число.** Это внутренний монотонно растущий счётчик Citus
   (следующий свободный `colocationid` в кластере), а не характеристика
   схемы: значение зависит от того, сколько групп колокации кластер успел
   создать раньше за свою жизнь. Дословный вывод команды честен как есть,
   но конкретное число нельзя переносить в статью как факт о дизайне —
   в прогоне Task 7 оно было 5, у ревьюера — 7, в предыдущей записи этого
   файла — 11, в нынешней — 20. **Рост от 11 к 20 между двумя записями
   FIXTURES — прямая иллюстрация к этому пункту:** схема не менялась,
   изменилось только число ранее созданных групп. Важен сам факт ОТДЕЛЬНОЙ
   группы (она не совпадает с `colocationid` таблицы `shipments`, равным 2),
   а не значение идентификатора.

## Артефакт 4: пагинация (LIMIT/OFFSET) поверх шардов

Команда: `bash scripts/pagination-demo.sh`. Собственная таблица
`orders_big` на 1 000 000 строк (32 шарда, от 29 740 до 33 400 строк на
шард), которую создаёт, наполняет и удаляет сам скрипт (объём подобран так,
чтобы максимальный запрошенный `OFFSET + LIMIT` = 25 020 гарантированно
помещался внутрь наименьшего шарда — без этого рост насыщается и артефакт
перестаёт быть показательным, см. README и Task 5). Две серии запроса с
одним и тем же `LIMIT 20`, отличающиеся сортировкой: серия А — `ORDER BY
total DESC` (НЕ по ключу шардирования), серия Б — `ORDER BY customer_id`
(ключ шардирования; PK `orders_big` начинается с `customer_id`, поэтому
серии Б доступен `Index Scan`). `OFFSET` пробегает 0 → 1000 → 10000 →
25000; на каждую точку — 7 повторов, берётся медиана; всего 3 независимых
прогона внутри одного запуска скрипта.

```
[pagination] Preflight: проверяем стенд перед созданием собственного набора данных…
[pagination] Кластер: воркеров 2. Стенд на месте.
[pagination] Preflight: если orders_big осталась от прерванного прогона — удаляем перед началом…
NOTICE:  table "orders_big" does not exist, skipping
[pagination] Создаём orders_big и распределяем по customer_id (отдельная группа колокации)…
[pagination] Наполняем orders_big: 1000000 строк одним INSERT ... SELECT generate_series…
[pagination] Наполнение заняло 5 с.
[pagination] orders_big: 1000000 строк, 32 шардов, partmethod='h'.
[pagination] Строк на шард: минимум 29740, максимум 33400, в среднем 31250.
[pagination] Максимальный запрошенный хвост: OFFSET 25000 + LIMIT 20 = 25020 строк с каждого шарда.
[pagination] OK: запрошенный хвост (25020) помещается внутрь наименьшего шарда (29740) — рост не насыщается, замер осмыслен.

================================================================
 Прогон 1 / 3
================================================================

--- Серия А: ORDER BY total DESC — НЕ по ключу шардирования ---
[pagination] Прогон 1, серия A: прогрев (результат отбрасывается)…

План (прогон 1, повтор 1, серия A, OFFSET=0) для иллюстрации:
                                                                                 QUERY PLAN
----------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=2660.96..2661.01 rows=20 width=24) (actual time=101.350..101.353 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total
   ->  Sort  (cost=2660.96..2910.96 rows=100000 width=24) (actual time=101.349..101.350 rows=20 loops=1)
         Output: remote_scan.order_id, remote_scan.total
         Sort Key: remote_scan.total DESC
         Sort Method: top-N heapsort  Memory: 26kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=24) (actual time=101.229..101.251 rows=640 loops=1)
               Output: remote_scan.order_id, remote_scan.total
               Task Count: 32
               Tuple data received from nodes: 12 kB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total FROM public.orders_big_103089 orders_big WHERE true ORDER BY total DESC LIMIT '20'::bigint
                     Tuple data received from node: 400 bytes
                     Node: host=citus-w1 port=5432 dbname=shard
                     ->  Limit  (cost=1380.65..1380.70 rows=20 width=24) (actual time=11.448..11.451 rows=20 loops=1)
                           Output: order_id, total
                           ->  Sort  (cost=1380.65..1457.45 rows=30720 width=24) (actual time=11.448..11.449 rows=20 loops=1)
                                 Output: order_id, total
                                 Sort Key: orders_big.total DESC
                                 Sort Method: top-N heapsort  Memory: 26kB
                                 ->  Seq Scan on public.orders_big_103089 orders_big  (cost=0.00..563.20 rows=30720 width=24) (actual time=0.007..5.477 rows=30120 loops=1)
                                       Output: order_id, total
                         Planning Time: 0.170 ms
                         Execution Time: 11.461 ms
 Planning Time: 1.776 ms
 Execution Time: 101.405 ms
(27 rows)
[pagination] Прогон 1, серия A, OFFSET=0: медиана=102.529 мс (min=94.676, max=108.494); строк с шардов=640; из 7 повторов: 101.405 106.633 108.494 102.529 94.676 106.649 100.540

План (прогон 1, повтор 1, серия A, OFFSET=1000) для иллюстрации:
                                                                                 QUERY PLAN
----------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=5499.68..5499.73 rows=20 width=24) (actual time=146.476..146.478 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total
   ->  Sort  (cost=5497.18..5747.18 rows=100000 width=24) (actual time=146.420..146.452 rows=1020 loops=1)
         Output: remote_scan.order_id, remote_scan.total
         Sort Key: remote_scan.total DESC
         Sort Method: top-N heapsort  Memory: 90kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=24) (actual time=139.110..140.357 rows=32640 loops=1)
               Output: remote_scan.order_id, remote_scan.total
               Task Count: 32
               Tuple data received from nodes: 637 kB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total FROM public.orders_big_103093 orders_big WHERE true ORDER BY total DESC LIMIT '1020'::bigint
                     Tuple data received from node: 20 kB
                     Node: host=citus-w1 port=5432 dbname=shard
                     ->  Limit  (cost=2251.93..2254.48 rows=1020 width=24) (actual time=14.883..14.979 rows=1020 loops=1)
                           Output: order_id, total
                           ->  Sort  (cost=2251.93..2328.73 rows=30720 width=24) (actual time=14.882..14.926 rows=1020 loops=1)
                                 Output: order_id, total
                                 Sort Key: orders_big.total DESC
                                 Sort Method: top-N heapsort  Memory: 115kB
                                 ->  Seq Scan on public.orders_big_103093 orders_big  (cost=0.00..563.20 rows=30720 width=24) (actual time=0.013..4.756 rows=31340 loops=1)
                                       Output: order_id, total
                         Planning Time: 0.094 ms
                         Execution Time: 15.039 ms
 Planning Time: 1.938 ms
 Execution Time: 146.930 ms
(27 rows)
[pagination] Прогон 1, серия A, OFFSET=1000: медиана=138.746 мс (min=128.151, max=150.495); строк с шардов=32640; из 7 повторов: 146.930 138.746 135.109 134.147 128.151 150.495 141.774

План (прогон 1, повтор 1, серия A, OFFSET=10000) для иллюстрации:
                                                                                 QUERY PLAN
----------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=7170.30..7170.35 rows=20 width=24) (actual time=437.860..437.864 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total
   ->  Sort  (cost=7145.30..7395.30 rows=100000 width=24) (actual time=437.193..437.602 rows=10020 loops=1)
         Output: remote_scan.order_id, remote_scan.total
         Sort Key: remote_scan.total DESC
         Sort Method: top-N heapsort  Memory: 1414kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=24) (actual time=358.210..382.415 rows=320640 loops=1)
               Output: remote_scan.order_id, remote_scan.total
               Task Count: 32
               Tuple data received from nodes: 6256 kB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total FROM public.orders_big_103078 orders_big WHERE true ORDER BY total DESC LIMIT '10020'::bigint
                     Tuple data received from node: 195 kB
                     Node: host=citus-w2 port=5432 dbname=shard
                     ->  Limit  (cost=2714.20..2739.25 rows=10020 width=14) (actual time=46.459..47.837 rows=10020 loops=1)
                           Output: order_id, total
                           ->  Sort  (cost=2714.20..2790.60 rows=30560 width=14) (actual time=46.458..47.034 rows=10020 loops=1)
                                 Output: order_id, total
                                 Sort Key: orders_big.total DESC
                                 Sort Method: top-N heapsort  Memory: 1429kB
                                 ->  Seq Scan on public.orders_big_103078 orders_big  (cost=0.00..530.60 rows=30560 width=14) (actual time=0.046..4.842 rows=30560 loops=1)
                                       Output: order_id, total
                         Planning Time: 0.391 ms
                         Execution Time: 48.975 ms
 Planning Time: 1.745 ms
 Execution Time: 440.075 ms
(27 rows)
[pagination] Прогон 1, серия A, OFFSET=10000: медиана=449.172 мс (min=417.796, max=1121.615); строк с шардов=320640; из 7 повторов: 440.075 425.794 417.796 449.172 693.940 1121.615 887.261

План (прогон 1, повтор 1, серия A, OFFSET=25000) для иллюстрации:
                                                                                 QUERY PLAN
----------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=7867.90..7867.95 rows=20 width=24) (actual time=1725.762..1725.768 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total
   ->  Sort  (cost=7805.40..8055.40 rows=100000 width=24) (actual time=1723.314..1725.120 rows=25020 loops=1)
         Output: remote_scan.order_id, remote_scan.total
         Sort Key: remote_scan.total DESC
         Sort Method: top-N heapsort  Memory: 3026kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=24) (actual time=1409.514..1499.441 rows=800640 loops=1)
               Output: remote_scan.order_id, remote_scan.total
               Task Count: 32
               Tuple data received from nodes: 15 MB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total FROM public.orders_big_103080 orders_big WHERE true ORDER BY total DESC LIMIT '25020'::bigint
                     Tuple data received from node: 488 kB
                     Node: host=citus-w2 port=5432 dbname=shard
                     ->  Limit  (cost=3099.60..3162.15 rows=25020 width=14) (actual time=94.772..108.808 rows=25020 loops=1)
                           Output: order_id, total
                           ->  Sort  (cost=3099.60..3183.10 rows=33400 width=14) (actual time=94.771..107.151 rows=25020 loops=1)
                                 Output: order_id, total
                                 Sort Key: orders_big.total DESC
                                 Sort Method: quicksort  Memory: 2841kB
                                 ->  Seq Scan on public.orders_big_103080 orders_big  (cost=0.00..590.00 rows=33400 width=14) (actual time=0.016..3.963 rows=33400 loops=1)
                                       Output: order_id, total
                         Planning Time: 0.355 ms
                         Execution Time: 111.710 ms
 Planning Time: 2.756 ms
 Execution Time: 1735.701 ms
(27 rows)
[pagination] Прогон 1, серия A, OFFSET=25000: медиана=1740.576 мс (min=1695.331, max=1930.834); строк с шардов=800640; из 7 повторов: 1735.701 1740.576 1695.331 1930.834 1875.825 1887.462 1697.546

--- Серия Б: ORDER BY customer_id — ПО ключу шардирования (доп. проверка) ---
[pagination] Прогон 1, серия B: прогрев (результат отбрасывается)…

План (прогон 1, повтор 1, серия B, OFFSET=0) для иллюстрации:
                                                                                            QUERY PLAN
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=2660.96..2661.01 rows=20 width=32) (actual time=124.285..124.289 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
   ->  Sort  (cost=2660.96..2910.96 rows=100000 width=32) (actual time=124.282..124.284 rows=20 loops=1)
         Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
         Sort Key: remote_scan.worker_column_3
         Sort Method: top-N heapsort  Memory: 27kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=32) (actual time=124.153..124.183 rows=640 loops=1)
               Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
               Task Count: 32
               Tuple data received from nodes: 17 kB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total, customer_id AS worker_column_3 FROM public.orders_big_103070 orders_big WHERE true ORDER BY customer_id LIMIT '20'::bigint
                     Tuple data received from node: 560 bytes
                     Node: host=citus-w2 port=5432 dbname=shard
                     ->  Limit  (cost=0.29..1.61 rows=20 width=22) (actual time=0.198..0.828 rows=20 loops=1)
                           Output: order_id, total, customer_id
                           ->  Index Scan using orders_big_pkey_103070 on public.orders_big_103070 orders_big  (cost=0.29..2109.23 rows=31860 width=22) (actual time=0.197..0.720 rows=20 loops=1)
                                 Output: order_id, total, customer_id
                         Planning Time: 0.351 ms
                         Execution Time: 0.846 ms
 Planning Time: 2.850 ms
 Execution Time: 124.389 ms
(23 rows)
[pagination] Прогон 1, серия B, OFFSET=0: медиана=126.927 мс (min=118.429, max=139.795); строк с шардов=640; из 7 повторов: 124.389 133.967 139.795 124.468 118.429 126.927 131.319

План (прогон 1, повтор 1, серия B, OFFSET=1000) для иллюстрации:
                                                                                             QUERY PLAN
-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=5499.68..5499.73 rows=20 width=32) (actual time=269.044..269.048 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
   ->  Sort  (cost=5497.18..5747.18 rows=100000 width=32) (actual time=268.975..269.019 rows=1020 loops=1)
         Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
         Sort Key: remote_scan.worker_column_3
         Sort Method: top-N heapsort  Memory: 127kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=32) (actual time=264.550..265.885 rows=32640 loops=1)
               Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
               Task Count: 32
               Tuple data received from nodes: 892 kB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total, customer_id AS worker_column_3 FROM public.orders_big_103083 orders_big WHERE true ORDER BY customer_id LIMIT '1020'::bigint
                     Tuple data received from node: 28 kB
                     Node: host=citus-w1 port=5432 dbname=shard
                     ->  Limit  (cost=0.29..67.82 rows=1020 width=22) (actual time=0.022..6.685 rows=1020 loops=1)
                           Output: order_id, total, customer_id
                           ->  Index Scan using orders_big_pkey_103083 on public.orders_big_103083 orders_big  (cost=0.29..1969.44 rows=29740 width=22) (actual time=0.021..6.349 rows=1020 loops=1)
                                 Output: order_id, total, customer_id
                         Planning Time: 0.106 ms
                         Execution Time: 9.326 ms
 Planning Time: 2.764 ms
 Execution Time: 269.770 ms
(23 rows)
[pagination] Прогон 1, серия B, OFFSET=1000: медиана=263.518 мс (min=219.976, max=312.288); строк с шардов=32640; из 7 повторов: 269.770 265.991 219.976 249.364 231.405 312.288 263.518

План (прогон 1, повтор 1, серия B, OFFSET=10000) для иллюстрации:
                                                                                              QUERY PLAN
------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=7170.30..7170.35 rows=20 width=32) (actual time=968.332..968.337 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
   ->  Sort  (cost=7145.30..7395.30 rows=100000 width=32) (actual time=966.184..967.611 rows=10020 loops=1)
         Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
         Sort Key: remote_scan.worker_column_3
         Sort Method: top-N heapsort  Memory: 1830kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=32) (actual time=862.904..911.630 rows=320640 loops=1)
               Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
               Task Count: 32
               Tuple data received from nodes: 8761 kB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total, customer_id AS worker_column_3 FROM public.orders_big_103087 orders_big WHERE true ORDER BY customer_id LIMIT '10020'::bigint
                     Tuple data received from node: 274 kB
                     Node: host=citus-w1 port=5432 dbname=shard
                     ->  Limit  (cost=0.29..662.77 rows=10020 width=22) (actual time=0.036..6.200 rows=10020 loops=1)
                           Output: order_id, total, customer_id
                           ->  Index Scan using orders_big_pkey_103087 on public.orders_big_103087 orders_big  (cost=0.29..2135.83 rows=32300 width=22) (actual time=0.036..5.566 rows=10020 loops=1)
                                 Output: order_id, total, customer_id
                         Planning Time: 0.361 ms
                         Execution Time: 29.183 ms
 Planning Time: 2.655 ms
 Execution Time: 972.430 ms
(23 rows)
[pagination] Прогон 1, серия B, OFFSET=10000: медиана=939.603 мс (min=575.523, max=1094.347); строк с шардов=320640; из 7 повторов: 972.430 965.014 916.457 939.603 1094.347 923.724 575.523

План (прогон 1, повтор 1, серия B, OFFSET=25000) для иллюстрации:
                                                                                              QUERY PLAN
-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
 Limit  (cost=7867.90..7867.95 rows=20 width=32) (actual time=1070.466..1070.471 rows=20 loops=1)
   Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
   ->  Sort  (cost=7805.40..8055.40 rows=100000 width=32) (actual time=1068.303..1069.890 rows=25020 loops=1)
         Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
         Sort Key: remote_scan.worker_column_3
         Sort Method: external merge  Disk: 26696kB
         ->  Custom Scan (Citus Adaptive)  (cost=0.00..0.00 rows=100000 width=32) (actual time=814.184..882.183 rows=800640 loops=1)
               Output: remote_scan.order_id, remote_scan.total, remote_scan.worker_column_3
               Task Count: 32
               Tuple data received from nodes: 21 MB
               Tasks Shown: One of 32
               ->  Task
                     Query: SELECT order_id, total, customer_id AS worker_column_3 FROM public.orders_big_103094 orders_big WHERE true ORDER BY customer_id LIMIT '25020'::bigint
                     Tuple data received from node: 684 kB
                     Node: host=citus-w2 port=5432 dbname=shard
                     ->  Limit  (cost=0.29..1659.68 rows=25020 width=22) (actual time=0.025..17.229 rows=25020 loops=1)
                           Output: order_id, total, customer_id
                           ->  Index Scan using orders_big_pkey_103094 on public.orders_big_103094 orders_big  (cost=0.29..2061.59 rows=31080 width=22) (actual time=0.024..14.200 rows=25020 loops=1)
                                 Output: order_id, total, customer_id
                         Planning Time: 0.502 ms
                         Execution Time: 24.518 ms
 Planning Time: 2.726 ms
 Execution Time: 1081.081 ms
(23 rows)
[pagination] Прогон 1, серия B, OFFSET=25000: медиана=973.186 мс (min=906.007, max=1081.081); строк с шардов=800640; из 7 повторов: 1081.081 952.608 1012.325 973.186 991.790 957.839 906.007

================================================================
 Прогон 2 / 3
================================================================

--- Серия А: ORDER BY total DESC — НЕ по ключу шардирования ---
[pagination] Прогон 2, серия A: прогрев (результат отбрасывается)…
[pagination] Прогон 2, серия A, OFFSET=0: медиана=104.210 мс (min=96.405, max=107.493); строк с шардов=640; из 7 повторов: 96.405 97.561 105.730 100.404 107.493 105.031 104.210
[pagination] Прогон 2, серия A, OFFSET=1000: медиана=136.161 мс (min=133.873, max=202.348); строк с шардов=32640; из 7 повторов: 134.956 202.348 136.161 143.912 152.851 133.873 134.022
[pagination] Прогон 2, серия A, OFFSET=10000: медиана=416.146 мс (min=402.650, max=437.411); строк с шардов=320640; из 7 повторов: 416.146 412.719 437.411 402.650 426.905 416.064 425.792
[pagination] Прогон 2, серия A, OFFSET=25000: медиана=795.233 мс (min=769.545, max=834.961); строк с шардов=800640; из 7 повторов: 834.961 775.893 773.408 769.545 800.113 801.744 795.233

--- Серия Б: ORDER BY customer_id — ПО ключу шардирования (доп. проверка) ---
[pagination] Прогон 2, серия B: прогрев (результат отбрасывается)…
[pagination] Прогон 2, серия B, OFFSET=0: медиана=56.821 мс (min=55.265, max=61.394); строк с шардов=640; из 7 повторов: 55.701 57.923 56.314 56.821 57.770 55.265 61.394
[pagination] Прогон 2, серия B, OFFSET=1000: медиана=114.974 мс (min=109.665, max=131.508); строк с шардов=32640; из 7 повторов: 114.853 117.610 109.665 119.796 131.508 114.974 114.008
[pagination] Прогон 2, серия B, OFFSET=10000: медиана=393.967 мс (min=386.027, max=428.230); строк с шардов=320640; из 7 повторов: 393.967 386.027 394.197 386.258 397.278 389.301 428.230
[pagination] Прогон 2, серия B, OFFSET=25000: медиана=886.383 мс (min=863.705, max=997.727); строк с шардов=800640; из 7 повторов: 906.849 869.490 997.727 883.788 886.383 863.705 887.430

================================================================
 Прогон 3 / 3
================================================================

--- Серия А: ORDER BY total DESC — НЕ по ключу шардирования ---
[pagination] Прогон 3, серия A: прогрев (результат отбрасывается)…
[pagination] Прогон 3, серия A, OFFSET=0: медиана=93.098 мс (min=85.953, max=134.784); строк с шардов=640; из 7 повторов: 91.779 93.098 103.013 85.953 97.629 92.594 134.784
[pagination] Прогон 3, серия A, OFFSET=1000: медиана=130.661 мс (min=127.699, max=147.969); строк с шардов=32640; из 7 повторов: 127.699 129.197 133.651 147.969 132.823 130.661 129.890
[pagination] Прогон 3, серия A, OFFSET=10000: медиана=415.059 мс (min=403.722, max=445.989); строк с шардов=320640; из 7 повторов: 445.989 430.531 415.059 403.722 404.988 411.168 434.073
[pagination] Прогон 3, серия A, OFFSET=25000: медиана=798.592 мс (min=770.128, max=831.143); строк с шардов=800640; из 7 повторов: 831.143 788.617 802.161 800.459 798.592 770.128 793.327

--- Серия Б: ORDER BY customer_id — ПО ключу шардирования (доп. проверка) ---
[pagination] Прогон 3, серия B: прогрев (результат отбрасывается)…
[pagination] Прогон 3, серия B, OFFSET=0: медиана=54.951 мс (min=53.937, max=56.287); строк с шардов=640; из 7 повторов: 54.951 53.937 54.331 55.001 54.553 56.189 56.287
[pagination] Прогон 3, серия B, OFFSET=1000: медиана=108.173 мс (min=104.418, max=121.295); строк с шардов=32640; из 7 повторов: 106.214 107.125 112.418 104.418 108.173 121.295 111.056
[pagination] Прогон 3, серия B, OFFSET=10000: медиана=386.988 мс (min=384.466, max=420.304); строк с шардов=320640; из 7 повторов: 387.947 385.269 401.150 384.466 386.988 420.304 386.841
[pagination] Прогон 3, серия B, OFFSET=25000: медиана=887.330 мс (min=849.735, max=1031.883); строк с шардов=800640; из 7 повторов: 912.682 887.330 857.992 849.735 1031.883 933.664 867.560

================================================================
 Набор данных
================================================================
Строк в orders_big:  1000000
Шардов:              32
Строк на шард:       минимум 29740, максимум 33400, в среднем 31250
Максимальный хвост:  OFFSET 25000 + LIMIT 20 = 25020 (помещается в наименьший шард)

================================================================
 Структурный признак: сколько строк координатор поднял с шардов
================================================================
Величина детерминированная: обязана быть РОВНО shard_count × (OFFSET + LIMIT).
Она и есть прямое доказательство утверждения артефакта — каждый шард
отдаёт координатору свои (OFFSET+LIMIT) кандидатов, независимо от того,
что из них попадёт в ответ (а попадёт всегда только 20 строк).

OFFSET    Ожидалось       А: факт           Б: факт         Объём (А)
0         640             640               640             12 kB
1000      32640           32640             32640           637 kB
10000     320640          320640            320640          6256 kB
25000     800640          800640            800640          15 MB

Полезных строк в ответе при этом всегда 20 — всё остальное поднято
с шардов и отброшено координатором. При OFFSET=25000 это
800640 строк ради 20.

================================================================
 Сводка: медиана (min–max) времени выполнения, мс
================================================================

-- Прогон 1 --
OFFSET    А: не по ключу (total DESC)   Б: по ключу (customer_id)
0         102.529 (94.676–108.494)      126.927 (118.429–139.795)
1000      138.746 (128.151–150.495)     263.518 (219.976–312.288)
10000     449.172 (417.796–1121.615)    939.603 (575.523–1094.347)
25000     1740.576 (1695.331–1930.834)  973.186 (906.007–1081.081)

-- Прогон 2 --
OFFSET    А: не по ключу (total DESC)   Б: по ключу (customer_id)
0         104.210 (96.405–107.493)      56.821 (55.265–61.394)
1000      136.161 (133.873–202.348)     114.974 (109.665–131.508)
10000     416.146 (402.650–437.411)     393.967 (386.027–428.230)
25000     795.233 (769.545–834.961)     886.383 (863.705–997.727)

-- Прогон 3 --
OFFSET    А: не по ключу (total DESC)   Б: по ключу (customer_id)
0         93.098 (85.953–134.784)       54.951 (53.937–56.287)
1000      130.661 (127.699–147.969)     108.173 (104.418–121.295)
10000     415.059 (403.722–445.989)     386.988 (384.466–420.304)
25000     798.592 (770.128–831.143)     887.330 (849.735–1031.883)

================================================================
 Падающий вариант (что было бы, если бы OFFSET ничего не стоил)
================================================================
Если бы координатор мог отбросить первые OFFSET строк, не поднимая их
с шардов, то:
  число строк, поднятых с шардов, не зависело бы от OFFSET и
  равнялось бы 640 при любом смещении.
ГЕЙТУЕТСЯ ИМЕННО ЭТО, и только это.

⚠️ Время в падающий вариант НЕ входит. Раньше вторым условием стояло
«медианное время серии А было бы примерно одинаковым для всех OFFSET», и
оно тоже гейтовалось. Условие снято: на однохостовом стенде время шумит
настолько, что этот скрипт дважды подряд упал у внешнего ревьюера при
точно воспроизведённой структуре. Кратность, монотонность, межпрогонный
разброс и направление А/Б печатаются ниже как наблюдения и на код
возврата не влияют.

================================================================
 Самопроверка
================================================================
[OK] Во всех 3 прогонах и в обеих сериях число строк, поднятых с шардов,
     ТОЧНО равно 32 × (OFFSET + 20): от 640 при OFFSET=0
     до 800640 при OFFSET=25000. Рост линеен по OFFSET и кратен числу шардов.
[OK] Во всех 3 прогонах на всех OFFSET: серия А — Seq Scan + сортировка внутри шарда;
     серия Б — Index Scan по PK, шард НЕ сортирует. Это структурное различие серий.
[OK] Метод сортировки на координаторе при OFFSET=25000 различается структурно во всех 3 прогонах:
     прогон 1: серия А — в памяти ('top-N heapsort  Memory: 3026kB'), серия Б — на диске ('external merge  Disk: 26696kB').
     прогон 2: серия А — в памяти ('top-N heapsort  Memory: 3009kB'), серия Б — на диске ('external merge  Disk: 26696kB').
     прогон 3: серия А — в памяти ('top-N heapsort  Memory: 3006kB'), серия Б — на диске ('external merge  Disk: 26696kB').
     ⚠️ Килобайты в этих строках НЕ цитировать: они зависят от work_mem (здесь 4MB)
     и плавают между прогонами. Гейтуется только «в памяти против на диске».

----------------------------------------------------------------
 Диагностика по времени (НЕ гейты: на код возврата не влияет)
----------------------------------------------------------------
Время на однохостовом стенде шумит: кратность и направление меняются от
прогона к прогону. Ниже — фактические числа как наблюдение, а не как
критерий успеха. Доказательная часть артефакта — структурные величины выше.
[наблюдение] Прогон 1, серия А: медиана выросла в 16.98 раза (102.529→1740.576 мс) — не меньше ориентира 3×.
[наблюдение] Прогон 2, серия А: медиана выросла в 7.63 раза (104.210→795.233 мс) — не меньше ориентира 3×.
[наблюдение] Прогон 3, серия А: медиана выросла в 8.58 раза (93.098→798.592 мс) — не меньше ориентира 3×.
[наблюдение] Прогон 1, серия А: медианы росли монотонно по OFFSET.
[наблюдение] Прогон 2, серия А: медианы росли монотонно по OFFSET.
[наблюдение] Прогон 3, серия А: медианы росли монотонно по OFFSET.
[наблюдение] OFFSET=0, серия А: разброс медиан между 3 прогонами = 11.112 мс; наименьший наблюдаемый рост (0→25000) = 691.023 мс.
[наблюдение] OFFSET=25000, серия А: разброс медиан между 3 прогонами = 945.343 мс; наименьший наблюдаемый рост (0→25000) = 691.023 мс.
[ПРЕДУПРЕЖДЕНИЕ] Разброс между прогонами (945.343 мс) не меньше наименьшего наблюдаемого роста (691.023 мс) — на этом хосте время плавает сильнее самого эффекта, усреднять его нельзя. На структурный вывод это не влияет.
[ПРЕДУПРЕЖДЕНИЕ] Прогон 1, OFFSET=0: серия Б (126.927 мс) НЕ быстрее серии А (102.529 мс) — ожидаемое направление на мелком смещении не воспроизвелось в этом прогоне.
[ПРЕДУПРЕЖДЕНИЕ] Прогон 1, OFFSET=25000: серия Б (973.186 мс) НЕ медленнее серии А (1740.576 мс) — ожидаемое направление на глубоком смещении не воспроизвелось в этом прогоне.
[наблюдение] Прогон 2, OFFSET=0: серия Б быстрее серии А (56.821 против 104.210 мс) — доступ по индексу внутри шарда окупается на первой странице.
[наблюдение] Прогон 2, OFFSET=25000: серия Б медленнее серии А (886.383 против 795.233 мс) — на глубокой странице выигрыш съедается сортировкой на координаторе.
[наблюдение] Прогон 3, OFFSET=0: серия Б быстрее серии А (54.951 против 93.098 мс) — доступ по индексу внутри шарда окупается на первой странице.
[наблюдение] Прогон 3, OFFSET=25000: серия Б медленнее серии А (887.330 против 798.592 мс) — на глубокой странице выигрыш съедается сортировкой на координаторе.

================================================================
 Меняется ли картина при сортировке по ключу шардирования?
================================================================
orders_big распределена ХЕШЕМ (partmethod='h'): строки с соседними
значениями customer_id физически разбросаны по всем 32 шардам, а не
лежат в них непересекающимися диапазонами.

-- Сравнение ПЛАНОВ (структура надёжнее времени), OFFSET=25000 --
Признак                           А: ORDER BY total DESC        Б: ORDER BY customer_id
Task Count                        32                            32
Строк поднято с шардов            800640                        800640
Объём данных с шардов             15 MB                         21 MB
Доступ внутри шарда               Seq Scan                      Index Scan
Шард сортирует у себя             да                            нет
Сортировка на координаторе        top-N heapsort  Memory: 3026kB external merge  Disk: 26696kB

-- Сравнение ВРЕМЕНИ (медианы, мс) --
Прогон    А: OFFSET 0     А: OFFSET 25000   Б: OFFSET 0     Б: OFFSET 25000
1         102.529         1740.576          126.927         973.186
2         104.210         795.233           56.821          886.383
3         93.098          798.592           54.951          887.330

Прогон 1: рост серии Б (по ключу) = 7.67×; серии А (не по ключу) = 16.98×.
Прогон 2: рост серии Б (по ключу) = 15.60×; серии А (не по ключу) = 7.63×.
Прогон 3: рост серии Б (по ключу) = 16.15×; серии А (не по ключу) = 8.58×.

Кратности по всем 3 прогонам: серия А — от 7.63× до 16.98×;
серия Б — от 7.67× до 16.15×.
⚠️ Ни та, ни другая кратность НЕ является результатом артефакта.
Порог 3× у серии А остался только ориентиром для диагностики и прогон
больше не валит. У серии Б кратность считается от очень малой базы
(OFFSET=0, десятки миллисекунд), поэтому небольшой разброс знаменателя даёт
большой разброс частного — это арифметика отношения, а не установленная
причина: природа самих выбросов здесь НЕ измерялась. Выброс в этой точке
смещает всё отношение — наблюдался прогон,
где база подскочила примерно с 60 до 98 мс и кратность просела с ~15× до
~10×.

⚠️ НИ КРАТНОСТЬ, НИ НАПРАВЛЕНИЕ БОЛЬШЕ НЕ ГЕЙТУЮТСЯ. Раньше направление
считалось воспроизводимым и валило прогон при несовпадении. Это оказалось
неверно: у внешнего ревьюера скрипт дважды подряд упал на этом же стенде,
во втором запуске ожидаемое направление менялось трижды, а рост серии А
по всем известным прогонам гулял от 3.64× до 16.87× — при том что структурный результат
(32 × (OFFSET + 20) поднятых строк) воспроизвёлся точно оба раза.
Поэтому временные величины понижены до наблюдений, а гейтами остались
только структурные: поднятые строки, Task Count, способ доступа и метод
сортировки. Формулировка «рост воспроизводится» СНЯТА.

ЧТО ИЗ ЭТОГО СЛЕДУЕТ.

1) ГЛАВНЫЙ СТРУКТУРНЫЙ ПРИЗНАК У ОБЕИХ СЕРИЙ ОДИНАКОВ. И та, и другая
   поднимают с шардов ровно 32 × (OFFSET + 20) строк — числа в таблице
   структурного признака выше совпадают до единицы. Task Count тоже
   одинаков. Сортировка по ключу шардирования НЕ избавляет от переноса
   кандидатов на координатор: при хеш-распределении шарды не хранят
   диапазоны ключа по порядку, поэтому координатор всё равно обязан
   собрать и смержить кандидатов со ВСЕХ шардов. Это и есть ответ на
   вопрос «меняется ли картина» — по существу механики не меняется.

2) ПЛАНЫ ПРИ ЭТОМ РАЗЛИЧАЮТСЯ, и различие реальное (см. таблицу выше):
   - внутри шарда серия Б идёт Index Scan и НЕ сортирует у себя;
     серия А делает Seq Scan и сортирует шард у себя;
   - на координаторе серия Б тащит дополнительную колонку ключа
     сортировки, поэтому объём данных с шардов больше
     (21 MB против 15 MB), и её сортировка на координаторе
     идёт дороже: «external merge  Disk: 26696kB»
     против «top-N heapsort  Memory: 3026kB» у серии А.

   ⚠️ ДВЕ ОГОВОРКИ, БЕЗ КОТОРЫХ ЭТИ ДВА НАБЛЮДЕНИЯ ОБОБЩАТЬ НЕЛЬЗЯ:

   а) Index Scan у серии Б возможен потому, что PRIMARY KEY таблицы
      начинается с customer_id и потому уже даёт нужный порядок. Это
      свойство СХЕМЫ, а не общее свойство сортировки по ключу
      шардирования. На таблице, где подходящего индекса нет, серия Б
      сортировала бы шард у себя точно так же, как серия А, и выигрыша
      на первой странице не было бы.

   б) Уход сортировки координатора на диск зависит от work_mem. При
      большем work_mem сортировка серии Б уложилась бы в память, и
      проигрыш на глубоком смещении мог бы уменьшиться, исчезнуть или
      сменить знак. Здесь work_mem не варьировался, поэтому вывод про
      глубокие страницы привязан к текущей конфигурации стенда.

3) ПО ВРЕМЕНИ ЭТО ДАЁТ РАЗНОНАПРАВЛЕННЫЙ ЭФФЕКТ — и его важно назвать
   точно, а не сгладить. Числа ниже — медианы ПО ВСЕМ 3 прогонам, а не
   из одного показательного. На МЕЛКОМ смещении серия Б заметно БЫСТРЕЕ
   (56.821 против 102.529 мс при OFFSET=0): экономия на сортировке внутри
   шарда видна сразу. На БОЛЬШОМ смещении она, наоборот, МЕДЛЕННЕЕ
   (887.330 против 798.592 мс при OFFSET=25000): к этому моменту доминирует
   сортировка на координаторе, а она у Б дороже.

   ⚠️ Это направление НЕ является воспроизводимым результатом и НЕ
   гейтуется: у внешнего ревьюера оно менялось трижды внутри одного
   запуска. Воспроизводимо здесь только структурное — способ доступа,
   метод сортировки и объём поднятых строк.
   Кратность роста у серии Б при этом выше, чем у А
   (7.67–16.15× против 7.63–16.98×), но это следствие того, что у Б ниже
   старт и выше потолок, а НЕ признак худшей масштабируемости: объём
   поднятых с шардов данных у серий идентичен. Саму по себе кратность
   серии Б за результат принимать не следует (см. оговорку выше).

ВЫВОД: сортировка по ключу шардирования — НЕ лекарство от дорогой
пагинации вглубь. При подходящем индексе она удешевляет доступ ВНУТРИ
шарда и потому помогает на первых страницах, но не устраняет главную
причину — перенос OFFSET+LIMIT строк с КАЖДОГО шарда на координатор.
На глубоких страницах она не помогает вовсе и в этой конфигурации даже
слегка проигрывает. Отрицательный результат получен на объёме данных, где
эффект в принципе мог бы проявиться (все OFFSET внутри шарда, рост
кратный), а не на насыщенном замере, где разницы не было бы видно ни у
одной из серий.

================================================================
 Уборка
================================================================
[pagination] Удаляем orders_big явно (до завершения скрипта, не дожидаясь trap)…
[OK] orders_big удалена. В citus_tables ровно 4 таблицы
     (customers, orders, shipments, carriers) — стенд вернулся к исходному состоянию.
[pagination] Готово.
[pagination] Уборка: удаляем orders_big (существует только для этой демонстрации)…
```

Код выхода: `0`.

**Разбор.** Структурный признак — детерминированный и одинаковый у обеих
серий: координатор поднимает с каждого из 32 шардов ровно `OFFSET + LIMIT`
строк-кандидатов, из которых в ответе остаётся 20. При хеш-распределении
(`partmethod='h'`) шарды не хранят диапазоны ключа по порядку, поэтому
сортировка по ключу шардирования (серия Б) **не избавляет** от переноса
кандидатов на координатор — она устраняет сортировку ВНУТРИ шарда (доступ
по индексу вместо `Seq Scan` + сортировка), а не сам перенос. Отсюда
разнонаправленный эффект по времени: на мелком `OFFSET` серия Б заметно
быстрее (экономия на сортировке внутри шарда), на глубоком — медленнее
(сортировка на координаторе у Б тащит служебную колонку, объём с шардов
больше, и она уходит на диск, `external merge`, вместо укладывающегося в
память `top-N heapsort` у А).

**Падающий вариант.** Если бы координатор мог отбросить первые `OFFSET`
строк, не поднимая их с шардов, число строк, поднятых с шардов, не
зависело бы от `OFFSET` — оставалось бы 640. Вместо этого оно равно
`32 × (OFFSET + 20)` точно, во всех прогонах и обеих сериях. Именно это, а
НЕ рост времени, и опровергает падающий вариант.

## ⚠️ Артефакт 4: формулировка «рост воспроизводится» СНЯТА

**Прежняя редакция этого раздела и статьи называла рост медианы
воспроизводимым (6.3–8.0×) и гейтовала его. Это неверно, и гейты
переделаны.** У внешнего ревьюера `pagination-demo.sh` **дважды подряд упал**
(`exit=1`) на этом же стенде — падали именно временны́е гейты: строгая
монотонность медиан, межпрогонный разброс и ожидаемое направление
сравнения серий (во втором запуске направление менялось трижды). При этом
СТРУКТУРНЫЙ результат воспроизвёлся точно оба раза: `640 → 800640`, ровно
`32 × (OFFSET + 20)`.

Наблюдавшийся рост серии А по всем известным прогонам гуляет в диапазоне
**3.64×–16.98×**: 3.64–13.81× в прогонах внешнего ревьюера (провенанс
указан явно — это не замер на машине автора); 4.68–16.87×, 7.20–7.95×,
7.82–8.65×, 7.73–8.58× и 7.63–16.98× в пяти контрольных запусках на машине
автора. Диапазон «6.3–8.0×» был частным случаем нескольких удачных прогонов,
а не свойством эффекта.

⚠️ **Верхняя граница уже сдвинулась один раз прямо по ходу правок.** В
печатаемом тексте скрипта зафиксирована огибающая «3.64–16.87×», а
записанный выше прогон дал у серии А **16.98×** — то есть прогон, которым
этот блок и снят, СОБСТВЕННУЮ напечатанную в нём же огибающую превысил.
Это не противоречие в данных, а свойство любой захардкоженной границы
шумной величины: она устаревает следующим прогоном. Читать «3.64–16.87×» в
блоке следует как «диапазон, известный на момент правки скрипта», а не как
границы эффекта. Сам факт, что число вышло за рамку в первом же прогоне
после её установки, — лишнее подтверждение, что кратность здесь ничего не
доказывает и в статью как воспроизводимая величина идти не может.

**Это был дефект проектирования гейтов, а не сбой стенда.** Время на
однохостовом стенде шумит — об этом говорят все оговорки ниже, — и делать
шумную величину условием успеха означает, что демонстрация падает у любого,
кто её запустит. Пример обязан быть воспроизводимым. Поэтому:

- **строгими гейтами** (валят прогон) остались только структурные величины,
  и каждая проверяется во ВСЕХ трёх внутренних прогонах: число поднятых с
  шардов строк по формуле `32 × (OFFSET + LIMIT)`, `Task Count`, способ
  доступа внутри шарда (`Seq Scan` у А против `Index Scan` у Б), сортировка
  внутри шарда (есть у А, нет у Б) и метод сортировки на координаторе
  (в памяти у А против `external merge Disk` у Б), плюс гейты уборки;
- **диагностикой** (печатаются, код возврата не меняют) стали кратность
  роста, монотонность медиан, межпрогонный разброс и направление А/Б.

⚠️ Гейт метода сортировки тоже пришлось чинить отдельно: он брал
`S_COORDSORT[1:…]`, то есть проверял ОДИН прогон из трёх, объявляя при этом
контраст структурным (воспроизводимым). Утверждение было шире проверки — тот
же класс дефекта, что вычищается по всему файлу, допущенный в самом гейте.
Теперь гейт идёт циклом по всем прогонам и при расхождении сообщает, какой
прогон что дал.

**В блоке выше записан ШУМНЫЙ запуск, и это удачно.** В нём три
`[ПРЕДУПРЕЖДЕНИЕ]`: направление НЕ воспроизвелось дважды в первом внутреннем
прогоне (при `OFFSET=0` серия Б оказалась медленнее — 126.927 против
102.529 мс; при `OFFSET=25000` наоборот быстрее — 973.186 против
1740.576 мс), и межпрогонный разброс (945.343 мс) превысил наименьший
наблюдаемый рост (691.023 мс). В прогонах 2 и 3 все четыре проверки
направления сошлись штатно. **`RC=0`, потому что структура сошлась во всех
трёх прогонах** — число поднятых строк, `Task Count`, способ доступа и метод
сортировки.

Это ровно то поведение, ради которого гейты переделывались: при прежней
редакции этот запуск завершился бы с `exit=1` — трижды, — хотя доказательная
часть артефакта воспроизвелась полностью. Другие контрольные запуски прошли
без единого предупреждения. **Оба исхода законны:** шум времени больше не
выдаётся ни за подтверждение, ни за опровержение структуры. Пять запусков
подряд после переделки дали `RC=0`.

**⚠️ Обязательные оговорки.**

1. **Ни кратность серии Б, ни кратность серии А не являются результатом
   артефакта.** Обе — наблюдения. У Б кратность считается от очень малой
   базы (`OFFSET=0`, десятки миллисекунд), поэтому небольшой разброс
   знаменателя даёт большой разброс частного — арифметически, а не по
   установленной причине: 15.25–16.91×, 8.02–16.23×, 15.14–15.68×,
   14.77–15.08× и 7.67–16.15× в пяти контрольных запусках, 10.28–17.34× в прогонах
   исполнителя (Task 5). У А разброс
   не меньше (см. выше, 3.64–16.87×). Порог 3× остался в скрипте только
   ориентиром для диагностики. **Ни кратность, ни направление в статью
   переноситься не должны как воспроизводимые** — переносится структура.
2. **`Index Scan` у серии Б — свойство схемы `orders_big`, а не общее
   свойство «сортировка по ключу шардирования».** Он возможен потому, что
   `PRIMARY KEY` таблицы начинается с `customer_id`. На таблице без такого
   индекса серия Б сортировала бы шард у себя точно так же, как серия А, и
   выигрыша на мелком `OFFSET` не было бы.
3. **Уход сортировки координатора на диск у серии Б зависит от
   `work_mem`.** При большем `work_mem` сортировка могла бы уложиться в
   память, и проигрыш на глубоком `OFFSET` уменьшился бы, исчез или сменил
   знак. `work_mem` в этом прогоне не варьировался — вывод про глубокие
   страницы привязан к текущей конфигурации стенда.
4. Время — порядок величины, однохостовая оговорка действует так же, как
   в предыдущих артефактах: перенос 15–21 MB с 32 шардов на реальном
   кластере стоил бы заметно дороже, и разрыв между мелким и глубоким
   `OFFSET` был бы ещё больше.
5. Курсорная (keyset) пагинация как альтернатива `OFFSET` в этом артефакте
   **не измерялась** — упоминается в выводе только как гипотеза для
   отдельного стенда.
6. **`Memory:` у top-N heapsort серии А — плавающее число, не структурная
   величина.** Оно стоит в структурной таблице «Сравнение ПЛАНОВ» рядом с
   по-настоящему устойчивыми `Task Count` и «строк поднято», но, в отличие
   от них, зависит от `work_mem` и колеблется между прогонами: 2982kB,
   3033kB у ревьюера, 2924kB, 3118kB, а в трёх внутренних прогонах
   канонического запуска — 3026kB / 3009kB / 3006kB. Цитировать конкретное
   число килобайт нельзя, цитируем метод (`top-N heapsort` в памяти против
   `external merge` на диске у Б). Гейт метода сортировки (пункт выше)
   сравнивает именно «в памяти против на диске» и килобайты не проверяет.
   ⚠️ Запас у этого гейта не бесконечен: `work_mem` здесь 4096kB, а серия А
   держится около 2.9–3.1 MB — примерно четверть запаса. При большем объёме
   данных или меньшем `work_mem` сортировка А тоже ушла бы на диск, и
   контраст исчез бы. Это ограничение конфигурации, а не свойство Citus.
7. **Сравнение с обычным (нешардированным) PostgreSQL по скорости здесь
   запрещено** — см. глобальную оговорку в начале файла. Ванильный
   PostgreSQL в этом артефакте не запускается; кроме того, на объёме
   `orders_big` (1 млн строк на один узел, будь она нешардирована) простой
   PostgreSQL, скорее всего, обошёлся бы без переноса кандидатов вовсе —
   такое сравнение здесь не измерено и не может быть измерено этим стендом.

## Артефакт 5: добавление узла и ребаланс

Команда: `bash scripts/rebalance-demo.sh`. Требует стенда на ДВУХ воркерах
(проверено в preflight). Скрипт поднимает третий воркер (`citus-w3`,
compose-профиль `grow`), регистрирует его (`citus_add_node`), снимает
распределение размещений ДО и ПОСЛЕ добавления узла (падающий вариант: само
по себе добавление узла ничего не перемещает), запускает
`citus_rebalance_start()`, во время его работы каждую секунду шлёт ДВЕ пробы
по ключу шардирования — ЧИТАЮЩУЮ и ПИШУЩУЮ, обе в один и тот же переезжающий
шард, — снимает распределение после ребаланса и тут же
сверяет ФАКТИЧЕСКОЕ размещение шарда пробы с target'ом из плана (пост-гейт), затем
возвращает кластер к двум воркерам (`citus_drain_node` → ожидание
фактического опустошения → `citus_remove_node` → остановка контейнера) и
удаляет строки, вставленные пишущей пробой.

Вывод ниже снят **живым прогоном** этого артефакта (`job_id=30`) — на
текущей версии скрипта, где выход из цикла опроса стоит ДО проб (см. разбор
исключённых прогонов ниже). Числа взяты из этого прогона, а не перенесены из
предыдущих. Снимок без ручных правок содержания — нормализованы только
концевые пробелы (их добавляет `psql`, дополняя заголовки колонок) и
окончания строк (LF): иначе `git diff --check` считает дефектом каждую такую
строку. Сохранены и
служебные строки `docker compose` (включая предупреждение про orphan-контейнеры
других стендов на машине автора), и исходное выравнивание колонок.

Разбор чисел — после блока. Кратко: ключ пробы выбран по фактическому плану
(`customer_id = 3` → `orders` шард `102055`, переезжает `citus-w1` → `citus-w3`),
пост-гейт подтвердил переезд по `pg_dist_placement`, чтение 18/18 без отказов
(максимум 507 мс), запись 18/18 без отказов и без потери видимости (максимум
538 мс на итерации 1 при `job.state=scheduled`), наполнение `orders` вернулось
к исходным 4000 строкам. Длительность окна опроса **измерена часами** —
31529 мс; см. отдельный разбор ниже, он отменяет прежнюю цифру «~21–26 с».

```
[rebalance] Preflight: проверяем, что кластер сейчас на ДВУХ воркерах и citus-w3 не поднят…
[rebalance] Все нужные функции ребаланса на месте: citus_add_node,citus_drain_node,citus_rebalance_start,citus_rebalance_status,citus_remove_node,get_rebalance_progress
[rebalance] Наполнение на месте: orders = 4000 строк. Ключ пробы будет выбран из фактического плана перемещений (шаг 2).

================================================================
 Шаг 1 / 6: распределение шардов ДО добавления узла
================================================================
 nodename | placements
----------+------------
 citus-w1 |         49
 citus-w2 |         49
(2 rows)

[rebalance] citus-w1=49, citus-w2=49, итого размещений=98. carriers: 2 размещений на [citus-w1,citus-w2].

================================================================
 Шаг 2 / 6: поднимаем citus-w3 (compose-профиль grow) и регистрируем
================================================================
[rebalance] docker compose --profile grow up -d worker3…
time="2026-07-20T15:51:31+03:00" level=warning msg="Found orphan containers (bj-postgres, ds-connect, ds-kafka) for this project. If you removed or renamed this service in your compose file, you can run this command with the --remove-orphans flag to clean it up."
 Container citus-w2 Running
 Container citus-w1 Running
 Container citus-coord Running
 Container citus-w3 Creating
 Container citus-w3 Created
 Container citus-w3 Starting
 Container citus-w3 Started
[rebalance] citus-w3: healthy
[rebalance] citus_add_node('citus-w3', 5432)…
[rebalance] Зарегистрированы активные воркеры: citus-w1,citus-w2,citus-w3.

----------------------------------------------------------------
 План перемещений (get_rebalance_table_shards_plan) — до старта ребаланса
----------------------------------------------------------------
 table_name | shardid | sourcename | sourceport | targetname | targetport
------------+---------+------------+------------+------------+------------
 customers  |  102008 | citus-w1   |       5432 | citus-w3   |       5432
 orders     |  102040 | citus-w1   |       5432 | citus-w3   |       5432
 customers  |  102011 | citus-w2   |       5432 | citus-w3   |       5432
 orders     |  102043 | citus-w2   |       5432 | citus-w3   |       5432
 customers  |  102013 | citus-w1   |       5432 | citus-w3   |       5432
 orders     |  102045 | citus-w1   |       5432 | citus-w3   |       5432
 customers  |  102016 | citus-w2   |       5432 | citus-w3   |       5432
 orders     |  102048 | citus-w2   |       5432 | citus-w3   |       5432
 customers  |  102018 | citus-w1   |       5432 | citus-w3   |       5432
 orders     |  102050 | citus-w1   |       5432 | citus-w3   |       5432
 customers  |  102022 | citus-w2   |       5432 | citus-w3   |       5432
 orders     |  102054 | citus-w2   |       5432 | citus-w3   |       5432
 customers  |  102023 | citus-w1   |       5432 | citus-w3   |       5432
 orders     |  102055 | citus-w1   |       5432 | citus-w3   |       5432
 customers  |  102027 | citus-w2   |       5432 | citus-w3   |       5432
 orders     |  102059 | citus-w2   |       5432 | citus-w3   |       5432
 customers  |  102028 | citus-w1   |       5432 | citus-w3   |       5432
 orders     |  102060 | citus-w1   |       5432 | citus-w3   |       5432
 customers  |  102039 | citus-w2   |       5432 | citus-w3   |       5432
 orders     |  102071 | citus-w2   |       5432 | citus-w3   |       5432
 shipments  |  102073 | citus-w2   |       5432 | citus-w3   |       5432
 shipments  |  102072 | citus-w1   |       5432 | citus-w3   |       5432
 shipments  |  102075 | citus-w2   |       5432 | citus-w3   |       5432
 shipments  |  102074 | citus-w1   |       5432 | citus-w3   |       5432
 shipments  |  102077 | citus-w2   |       5432 | citus-w3   |       5432
 shipments  |  102076 | citus-w1   |       5432 | citus-w3   |       5432
 shipments  |  102079 | citus-w2   |       5432 | citus-w3   |       5432
 shipments  |  102078 | citus-w1   |       5432 | citus-w3   |       5432
 shipments  |  102081 | citus-w2   |       5432 | citus-w3   |       5432
 shipments  |  102080 | citus-w1   |       5432 | citus-w3   |       5432
(30 rows)

[rebalance] Шарды orders в плане перемещений: 102040,102043,102045,102048,102050,102054,102055,102059,102060,102071.

[ГЕЙТ OK] Ключ пробы выбран ПО ПЛАНУ, а не константой:
  customer_id = 3  ->  orders shardid = 102055
  Шард 102055 ВХОДИТ в план перемещений: citus-w1:5432 -> citus-w3:5432
  Перемещаемые шарды orders: [102040,102043,102045,102048,102050,102054,102055,102059,102060,102071]
  Ожидаемый ответ пробы: 20
[rebalance] Проба бьёт ИМЕННО в переезжающий шард orders=102055 (citus-w1:5432 -> citus-w3:5432); ожидаемый ответ 20.

================================================================
 Шаг 3 / 6: распределение ПОСЛЕ добавления citus-w3, ДО ребаланса
================================================================
Падающий вариант: если бы добавление узла само по себе перераспределяло
шарды, citus-w3 уже показал бы ненулевое число размещений — и ребаланс
был бы не нужен. Проверяется ниже.
 nodename | placements
----------+------------
 citus-w1 |         49
 citus-w2 |         49
(2 rows)

[rebalance] citus-w1=49, citus-w2=49, citus-w3=0, итого=98. carriers: 2 размещений на [citus-w1,citus-w2].

================================================================
 Шаг 4 / 6: запускаем ребаланс (citus_rebalance_start) — асинхронно
================================================================
[rebalance] Ребаланс запущен: job_id=30. (NOTICE:  Scheduled 20 moves as job 30
DETAIL:  Rebalance scheduled as background job
HINT:  To monitor progress, run: SELECT * FROM citus_rebalance_status();)

================================================================
 Шаг 6 / 6 (одновременно с шагом 5): пробы ЧТЕНИЯ и ЗАПИСИ по ключу шардирования
  во время выполнения ребаланса — фиксируем факт, а не предположение
================================================================
Проб ДВЕ, обе бьют в ОДИН и тот же переезжающий шард orders=102055:
  1. ЧИТАЮЩАЯ: SELECT count(*) FROM orders
               WHERE customer_id = 3 AND order_id < 900000000;  (ожидаем: 20)
  2. ПИШУЩАЯ:  INSERT INTO orders (customer_id, order_id, total)
               VALUES (3, 900000000 + N, ...);
               + проверка ВИДИМОСТИ: последующее чтение обязано вернуть увеличенное количество.
Этот customer_id лежит в шарде orders=102055, и этот шард ПЕРЕЕЗЖАЕТ:
  citus-w1:5432 -> citus-w3:5432
То есть пробы бьют в ПЕРЕЕЗЖАЮЩИЙ шард, а не в неподвижный.

ГРАНИЦА: доказано, что шард входил в план и после job'а оказался на
target-узле, а пробы шли всё время работы job'а. НЕ доказано, что хотя бы
одна проба пришлась ровно на короткое окно переключения ИМЕННО этого
шарда: скрипт следит за состоянием job'а целиком, а не за фазой
конкретного шарда. Пробы могли уложиться до или после его cutover.
Для более сильного утверждения нужна корреляция с get_rebalance_progress()
по выбранному шарду — здесь её нет.

ЗАЧЕМ ПИШУЩАЯ. Режимы переноса Citus противопоставляются по ЗАПИСЯМ:
логическая репликация ('auto') позволяет избежать блокировки записей,
'block_writes' копирует шард через COPY с их блокировкой. Чтение доступно
в ОБОИХ режимах, поэтому одна читающая проба преимущества 'auto' показать
не может — успешна она была бы в любом случае. Пишущая проба проверяет
УСПЕШНОЕ ЗАВЕРШЕНИЕ INSERT и ВИДИМОСТЬ записанного.

ЧЕГО ОНА НЕ ОПРЕДЕЛЯЕТ: наличие краткой блокировки. Заблокированный
INSERT обычно ЖДЁТ и потом завершается успешно — то есть проба его
засчитает. statement_timeout здесь не задан, поэтому долгая блокировка
не дала бы ошибку, а подвесила бы docker exec. И отказ сам по себе
блокировку не доказывает: для этого нужен разбор текста ошибки или
pg_locks, чего стенд не делает. Длительности печатаются сырыми и с
фазой переноса конкретного шарда не соотнесены.

  [1] job.state=scheduled | чтение: OK (rc=0, 507 мс, ответ='20') | запись: OK (rc=0, 538 мс, видно='1'/ожидали='1')
  [6] job.state=running | чтение: OK (rc=0, 108 мс, ответ='20') | запись: OK (rc=0, 115 мс, видно='6'/ожидали='6')
  [11] job.state=running | чтение: OK (rc=0, 113 мс, ответ='20') | запись: OK (rc=0, 115 мс, видно='11'/ожидали='11')
  [16] job.state=running | чтение: OK (rc=0, 114 мс, ответ='20') | запись: OK (rc=0, 133 мс, видно='16'/ожидали='16')

[rebalance] Опрос завершён. Итоговое состояние job'а: finished.
[rebalance] ЧТЕНИЕ: 18 попыток, успешных=18, неуспешных=0, максимум 507 мс.
[rebalance] ЗАПИСЬ: 18 попыток, успешных=18, неуспешных=0, максимум 538 мс (итерация 1, job.state=scheduled).
[rebalance] Наполнение orders: было 4000, во время пробы 4018, после удаления строк пробы 4000 (остаток строк пробы: 0).

================================================================
 Шаг 5 / 6: распределение шардов ПОСЛЕ ребаланса
================================================================
 nodename | placements
----------+------------
 citus-w1 |         34
 citus-w2 |         34
 citus-w3 |         31
(3 rows)

[rebalance] citus-w1=34, citus-w2=34, citus-w3=31, итого=99. carriers: 3 размещений на [citus-w1,citus-w2,citus-w3].

----------------------------------------------------------------
 ПОСТ-ГЕЙТ: фактическое размещение шарда пробы после ребаланса
----------------------------------------------------------------
  Шард пробы (orders):            102055  (customer_id = 3)
  План обещал target-узел:        citus-w3
  Факт (pg_dist_placement, shardstate=1): citus-w3
  [ПОСТ-ГЕЙТ OK] План и факт совпали — проба действительно читала шард, который переезжал на citus-w3.

================================================================
 Сводка распределения по трём снимкам
================================================================
Снимок                   citus-w1   citus-w2   citus-w3   Итого
1. До добавления узла 49         49         -          98
3. После добавления узла 49         49         0          98
5. После ребаланса 34         34         31         99

carriers (референсная) размещений узлы
1. До добавления узла 2          citus-w1,citus-w2
3. После добавления узла 2          citus-w1,citus-w2
5. После ребаланса 3          citus-w1,citus-w2,citus-w3

================================================================
 Уборка: возвращаем кластер к ДВУМ воркерам
================================================================
[rebalance] Уборка: гасим незавершённые фоновые задачи (иначе слив с ними конфликтует)…
[rebalance] Уборка: дренируем citus-w3 (citus_drain_node)…
[rebalance] Уборка: принудительно разбираем отложенные записи pg_dist_cleanup, пока citus-w3 ещё жив…
[rebalance] Уборка: снимаем регистрацию citus-w3 (citus_remove_node)…
[rebalance] Уборка: останавливаем и удаляем контейнер citus-w3…
[rebalance] pg_dist_node после уборки: citus-w1,citus-w2.
[rebalance] Контейнер citus-w3 после уборки: missing.
[rebalance] После уборки: citus-w1=49, citus-w2=49, итого=98. carriers: 2 размещений на [citus-w1,citus-w2]. pg_dist_cleanup=0.

================================================================
 Самопроверка
================================================================
[OK] Падающий вариант подтверждён: добавление узла само по себе НЕ переместило ни одного размещения (citus-w3=0 сразу после citus_add_node).
[OK] Ребаланс перераспределил размещения на три узла: было w1=49/w2=49 (всего 98), стало w1=34/w2=34/w3=31 (всего 99, без потерь).
[OK] Референсная таблица carriers: копию на новый узел кладёт ИМЕННО ребаланс, не citus_add_node — до ребаланса 2 размещения на [citus-w1,citus-w2], после 3 на [citus-w1,citus-w2,citus-w3].
[OK] ЧТЕНИЕ во время ребаланса: 18 попыток, успешных=18, неуспешных=0, максимум 507 мс.
[OK] ЗАПИСЬ во время ребаланса: 18 попыток, успешных=18, неуспешных=0, максимум 538 мс (итерация 1, job.state=scheduled).
     Успех пишущей пробы = INSERT прошёл И записанное сразу видно последующим чтением, а не просто «не отдал ошибку».
     Строки пробы удалены: orders вернулась к 4000 (исходное 4000).
     ⚠️ Именно ЗАПИСЬ отличает режимы переноса: логическая репликация ('auto')
     позволяет избежать блокировки записей, 'block_writes' копирует шард через
     COPY с их блокировкой. Чтение доступно в ОБОИХ режимах, поэтому одной
     читающей пробы для утверждения о преимуществе 'auto' не хватает.
     Обе пробы били в ПЕРЕЕЗЖАЮЩИЙ шард: customer_id=3 -> orders shardid=102055, citus-w1:5432 -> citus-w3:5432 (план перемещений orders: [102040,102043,102045,102048,102050,102054,102055,102059,102060,102071]).
[OK] ПОСТ-ГЕЙТ: план и факт сошлись — после завершения job'а активное размещение (shardstate=1) шарда пробы orders=102055 находится на 'citus-w3', ровно на том узле, который предварительный план указал как target ('citus-w3'). Проверка членства в плане подкреплена фактом.
     Ни одна ПИШУЩАЯ проба не отказала, и каждая записанная строка была видна следующим же чтением.
     Максимумы: чтение 507 мс, запись 538 мс (итерация 1, job.state=scheduled). Это сырые числа; природа отдельных задержек здесь НЕ установлена — в длительность входят docker exec и подключение, с фазой переноса конкретного шарда они не соотнесены.
     Ни один ЧИТАЮЩИЙ пробный запрос не отказал. Точная формулировка: во всех засчитанных итерациях job.state был НЕтерминальным, а цикл завершился при первом наблюдении 'finished'. Набор пройденных состояний скрипт не сохраняет и промежуточные фазы мог пропустить между опросами — утверждать, что job прошёл через все, нельзя.

     ⚠️ ГРАНИЦА ЭТОГО УТВЕРЖДЕНИЯ. Наблюдений здесь 18, по одному примерно
     раз в секунду (итерация — это sleep 1 ПЛЮС три обращения через docker exec,
     поэтому число проб к числу секунд не приравнивается). Длительность окна опроса
     ИЗМЕРЕНА часами в этом прогоне: 31529 мс от первого опроса состояния до
     первого наблюдения 'finished' (правая граница известна с точностью до интервала
     опроса). Это НЕ время фактического переноса шардов — см. оговорку выше.
     Эти пробы покрывают ВЕСЬ ребаланс на игрушечном объёме: 4000 заказов, ~20 перемещений.
     Читать это следует как «в коротком прогоне на маленьких данных ни одна
     проба не отказала», а НЕ как гарантию доступности вообще. На реальных
     объёмах ребаланс идёт часами, перемещений тысячи, и окно, в котором
     что-то может пойти не так, несоизмеримо шире.

     Отдельно про соблазн набрать побольше наблюдений: в одном из ранних
     прогонов этого стенда набралось 1200 успешных проб — но лишь потому,
     что ребаланс тогда ЗАВИС и почти всё это время шарды не двигались.
     Большая выборка измеряла в основном простой. Здесь 18 проб
     покрывают время жизни job'а от постановки до первого наблюдения его
     завершения. Это НЕ равно времени фактического переноса шардов: за
     прогрессом отдельных перемещений скрипт не следит, и внутрь окна попадают
     ожидание планировщика, копирование справочника (replicate_reference_tables)
     и возможные паузы между moves; правая граница известна с точностью до
     интервала опроса. Что доказано — многоминутного зависания в этом окне не
     было: этого хватает, чтобы отличить прогон от того самого, с 1200 пробами
     по зависшему ребалансу, и большего утверждать нельзя.
[OK] Уборка подтверждена: pg_dist_node вернулся к [citus-w1,citus-w2], контейнер citus-w3 удалён, число размещений (в т.ч. carriers) совпадает с исходным снимком до добавления узла.
[rebalance] Готово.
```

Код выхода: `0`.

**Разбор.** Регистрация нового узла (`citus_add_node`) сама по себе НЕ
двигает ни одного размещения — `citus-w3` остаётся с нулём и после
регистрации, пока `citus_rebalance_start()` не запущен явно. Ребаланс
переносит 20 запланированных перемещений (`job_id=30`, `Scheduled 20 moves`)
и завершается состоянием `finished`.

**⚠️ 98/99 — это ВСЕГО размещений (распределённых + референсных вместе),
не число распределённых.** До ребаланса кластер держит **98 размещений
всего**: **96 размещений распределённых таблиц** (`customers` + `orders` +
`shipments`, по 32 шарда каждая, репликации нет → 96 = 3 × 32; проверено
отдельным запросом с группировкой по `citus_table_type`:
`distributed | 96`) плюс **2 размещения референсной `carriers`** (по
одному на каждый из двух воркеров; `reference | 2`). Число **96 нигде
раньше в этом файле не появлялось** — оно нужно статье как отдельная
величина, а не выводится читателем из 98. После ребаланса общее число
размещений становится **99** (34+34+31): 96 размещений распределённых
таблиц остаются БЕЗ ПОТЕРЬ, просто перераскладываются по трём узлам
(33+1, 33+1, 30+1 — на каждом ВОРКЕРЕ к распределённым добавлена одна копия
`carriers`), а копий `carriers` становится 3 — ребаланс кладёт её и на
новый узел. Разница между 98 и 99 (плюс единица) — это НЕ потерянная и не
задвоенная строка данных, а появившаяся третья копия референсной таблицы;
смешивать общее число размещений (98/99) со специфически распределённым
(96/96, без изменений) нельзя — это два разных инварианта с разными
ожидаемыми значениями. Комментарий в `scripts/rebalance-demo.sh` (раздел
самопроверки «5. Ребаланс не должен терять/дублировать размещения»)
предостерегает ровно от этого: первая редакция проверки сравнивала общее
98 против 99, видела разницу и объявляла провал — потерю или задвоение
данных, — хотя оба числа были верны, просто про разное. Референсная
таблица `carriers` получает копию на новом узле НЕ от `citus_add_node`, а
от самого ребаланса — задачей `replicate_reference_tables`, идущей первой
(см. `rebalance-demo.sh` и Task 6): до ребаланса на `citus-w3` копий нет,
после — есть.

**Падающий вариант.** Если бы добавление узла само перераспределяло
данные, `citus-w3` показал бы ненулевые размещения уже на шаге 3, до
запуска ребаланса. Наблюдается `citus-w3=0` — падающий вариант не
воспроизвёлся, ребаланс структурно необходим.

**Доступность во время переноса — и почему проба бьёт именно туда, куда
нужно.** Ключ пробы НЕ фиксирован константой. Ранняя редакция скрипта
использовала `customer_id = 42`; его шарды (`customers=102035`,
`orders=102067`) в план перемещений НЕ входили, и успешные пробы доказывали
лишь доступность НЕПОДВИЖНОГО шарда во время работы фоновой задачи, а не
чтение данных В ПРОЦЕССЕ ИХ ПЕРЕНОСА. Это была подмена доказательства, и она
исправлена: скрипт после `citus_add_node` берёт ФАКТИЧЕСКИЙ план
(`get_rebalance_table_shards_plan()`), выбирает `customer_id`, чей шард
`orders` в этом плане есть и у которого есть заказы, и проверяет членство
шарда пробы в множестве перемещаемых **явным гейт-инвариантом**: если шард
пробы в плане не значится (или в плане вообще нет шардов `orders`) — скрипт
объявляет провал и завершается ненулевым кодом, а не продолжает молча.

**Гейтов теперь ДВА, и второй проверяет факт, а не план.** Первый (шаг 2)
сверяет шард пробы с ПРЕДВАРИТЕЛЬНЫМ планом — но план считается ДО
`citus_rebalance_start()` и теоретически может разойтись с исполненным, и
тогда успешные пробы снова доказывали бы не то. Поэтому после завершения
job'а (и обязательно ДО уборки, которая сливает `citus-w3` обратно) скрипт
берёт ФАКТИЧЕСКОЕ активное размещение шарда пробы по
`pg_dist_placement` + `pg_dist_node` (`shardstate = 1`) и сверяет узел с
target'ом из плана. Несовпадение — провал с ненулевым кодом. Проверяется
именно шард `orders` (тот, который читала проба): `orders` колоцирована с
`customers` (`colocation_id = 1`), перемещения идут группой колокации, и
ведущим в плане может стоять шард `customers` — факт нужен ровно про шард
пробы.

В этом прогоне гейт выбрал `customer_id = 3` → шард `orders=102055`, который
переезжает `citus-w1:5432 -> citus-w3:5432`; пост-гейт подтвердил, что после
завершения job'а активное размещение `102055` действительно на `citus-w3`.

**⚠️ Проб ДВЕ, и это исправление по итогам внешнего ревью.** Прежняя редакция
артефакта слала во время ребаланса только ЧИТАЮЩУЮ пробу и на её успехе
объявляла «перенос без блокировки чтения», противопоставляя это режиму
`block_writes`. Связка неверна. Документация Citus
(`citus_move_shard_placement`) противопоставляет режимы по ЗАПИСЯМ:
логическая репликация (`auto`/`force_logical`) позволяет избежать блокировки
ЗАПИСЕЙ, а `block_writes` копирует шард через `COPY` С БЛОКИРОВКОЙ ЗАПИСЕЙ.
Чтение доступно в ОБОИХ режимах, поэтому читающая проба структурно НЕ СПОСОБНА
показать преимущество `auto` — она была бы успешна и при `block_writes`. Это
была подмена доказательства: «проверили чтение → объявили неблокирующий
режим». Формулировка «без блокировки чтения» как отличие режимов убрана
везде.

Теперь в цикл опроса идут две пробы, обе по одному и тому же `PROBE_KEY`,
то есть в один и тот же переезжающий шард:

- **читающая** — `SELECT count(*) … WHERE customer_id = 3 AND order_id <
  900000000` (ограничение по `order_id` держит ожидаемый ответ стабильным,
  иначе вставки соседней пробы ломали бы её ожидание);
- **пишущая** — `INSERT INTO orders (customer_id, order_id, total) VALUES
  (3, 900000000 + N, 1.00)` и сразу за ним проверка ВИДИМОСТИ: чтение
  диапазона пробы обязано вернуть увеличенное количество. Успехом считается
  не «INSERT не отдал ошибку», а «записанное видно».

⚠️ **ВСЕ ПРОГОНЫ ДО `job_id=22` ИЗ НАБОРА ИСКЛЮЧЕНЫ.** В них проверка
терминального состояния стояла ПОСЛЕ проб, поэтому последняя итерация
успевала выполнить чтение и запись уже при `job.state=finished`. Такие
пробы попадали в счётчики и в максимумы наравне с остальными: выборка была
завышена на одну пробу каждого вида за прогон, а опубликованный максимум
мог относиться к моменту, когда переносить было уже нечего. Ретроактивно
почистить эти прогоны нельзя — по сохранённым данным посттерминальный
отсчёт не отделяется от остальных, — поэтому они не исправлены, а заменены.
Порядок в цикле исправлен: выход происходит ДО проб.

**Фактический результат, восемь прогонов на исправленном скрипте** (машина
автора, все пробы сняты при НЕзавершённом job'е; `job_id=30` — тот самый
прогон, что приведён выше как канонический вывод):

| Прогон | Проб | Чтение (успешно / отказало) | Макс. чтение | Запись (успешно / отказало) | Макс. запись |
|--------|------|------------------------------|--------------|------------------------------|--------------|
| `job_id=22` | 19 | 19 / 0 | 241 мс | 19 / 0 | 183 мс (итерация 2, `state=scheduled`) |
| `job_id=23` | 19 | 19 / 0 | 146 мс | 19 / 0 | 153 мс (итерация 2, `state=running`) |
| `job_id=24` | 20 | 20 / 0 | 894 мс | 20 / 0 | **1301 мс** (итерация 8, `state=running`) |
| `job_id=26` | 20 | 20 / 0 | 314 мс | 20 / 0 | 316 мс (итерация 17, `state=running`) |
| `job_id=27` | 19 | 19 / 0 | 145 мс | 19 / 0 | **959 мс** (итерация 12, `state=running`) |
| `job_id=29` | 21 | 21 / 0 | 163 мс | 21 / 0 | 190 мс (итерация 3, `state=scheduled`) |
| `job_id=30` (**канонический**) | 18 | 18 / 0 | 507 мс | 18 / 0 | 538 мс (итерация 1, `state=scheduled`) |
| `job_id=31` (снят внешним ревью, не на этой машине) | 18 | 18 / 0 | 603 мс | 18 / 0 | **2668 мс** (итерация 6, `state=running`) |

⚠️ Происхождение последней строки указано намеренно: остальные прогоны
набора сняты на машине автора, а этот — сторонним прогоном при ревью.
Данные по нему полные (проб, обе пробы, обе итерации, окно опроса 39928 мс,
уборка и восстановление прошли), поэтому он входит и в подсчёт проб, и в
подсчёт максимумов наравне с остальными.

Гейты на РЕЗУЛЬТАТ проб (8b) проверены дважды: на подставных значениях
(искусственный отказ чтения и отдельно записи — оба роняют `ok` в 0, чистый
набор оставляет 1) и живыми прогонами, все восемь прошли с `RC=0`.

Ни одна ЗАПИСЬ не отказала, и каждая записанная строка была видна следующим
же чтением. **Типичного значения длительности здесь нет и быть не может:**
скрипт печатает отсчёты не на каждой итерации (только на каждой пятой), а
сохраняет из прогона лишь максимум, поэтому ни медианы, ни квантилей по
набору не считается. Утверждать можно только максимумы и факт отсутствия
отказов; в любую длительность, кроме того, входят `docker exec` и
подключение `psql`, а не одно лишь время самого запроса.

Разовые всплески длительности есть, и в картину они НЕ складываются: в
четырёх прогонах (22, 23, 26, 29) максимумы обеих проб держались в пределах
145–316 мс; в `job_id=24` разом выскочили и чтение (894 мс), и запись
(1301 мс); в `job_id=27` выскочила ТОЛЬКО запись (959 мс при максимуме
чтения 145 мс); в `job_id=30` обе поднялись умеренно и вместе (507 и 538 мс),
причём максимум пришёлся на ПЕРВУЮ итерацию, ещё при `state=scheduled`;
в `job_id=31` (сторонний прогон) запись дала самый большой максимум набора —
2668 мс при максимуме чтения 603 мс. Отказов и потерь видимости при этом не
было ни разу.

Про максимум на первой итерации при `scheduled` можно сказать ровно одно: он
снят в момент, когда job ещё не начал переносить шарды, поэтому ОТНЕСТИ ЭТОТ
КОНКРЕТНЫЙ отсчёт к фазе переключения шарда нельзя. То же самое, но слабее,
видно в прогонах 22 и 29, где максимум записи тоже пришёлся на `scheduled`.
Правдоподобное объяснение — прогрев (первое `docker exec`, первое подключение
`psql`), но **здесь оно не проверено**: отдельного замера холодного и
прогретого вызова стенд не делает. Ни о наличии, ни об отсутствии блокировок
в остальных отсчётах это не говорит.

**Что показывает `job_id=27` — и чего он НЕ показывает.** В нём максимум
записи в 6.6 раза больше максимума чтения (959 против 145 мс) при нуле
отказов. Снятая эвристика скрипта («максимум записи больше максимума чтения
втрое → похоже на задержку записей при переключении шарда») на этих данных
объявила бы блокировку. **Это и есть содержание наблюдения: эвристика
срабатывает там, где оснований для вывода нет.**

⚠️ Обратное утверждение — «значит, блокировки не было» — из этого прогона
НЕ следует, и делать его нельзя. В том же файле выше сказано, почему:
заблокированный `INSERT` обычно ЖДЁТ и завершается успешно, `statement_timeout`
здесь не задан. Значит 959 мс совместимы И с краткой блокировкой, И с
посторонней задержкой (`docker exec`, подключение `psql`, что угодно ещё), а
ноль отказов не различает эти случаи. **Различать их стенд не умеет вообще:**
для этого нужен разбор `pg_locks` или корреляция с `get_rebalance_progress()`
по конкретному шарду, чего здесь нет. По набору прогонов соотношение
максимумов гуляет в обе стороны — ниже максимума чтения (22), вровень (26 и
29 и 30), выше в 1.5 раза (24), выше в 4.4 раза (31), выше в 6.6 раза (27), —
и ни одно из этих значений само по себе не свидетельствует ни за блокировку,
ни против неё.

**То же самое относится к `job_id=31` с его 2668 мс — рекордом набора.**
Соблазн прочитать «самая большая задержка записи, и всё равно ноль отказов»
как довод против блокировки надо погасить: успешный `INSERT` мог сначала
ждать блокировку, а потом завершиться, и статистика стенда этих двух случаев
не различает. **Единственный корректный вывод из 2668 мс — измерение
НЕОДНОЗНАЧНО:** без `pg_locks` и без корреляции с фазой конкретного шарда
величина задержки не говорит ни за наличие блокировки, ни против него. Ровно
так же, как 190 мс в `job_id=29` не «доказывают отсутствие» блокировки.

**Соотношение максимумов НЕ диагностирует блокировку, и прежняя попытка
трактовать его так убрана из скрипта.** Максимумы двух проб независимы,
меряются в разные моменты, в длительность входят `docker exec` и
подключение `psql` (а не только сам `INSERT`), и ни один отсчёт не соотнесён
с фазой переноса конкретного шарда. **Чем эти всплески вызваны — здесь НЕ
установлено.** Списывать их на «шум хоста» тоже было бы догадкой: такого
замера не делалось.

Что именно измерено, без интерпретации: **за прогоны 22/23/24/26/27/29/30/31 выполнено
154 пишущих пробы (19+19+20+20+19+21+18+18) и столько же читающих; ни одна не отказала и ни
одна не потеряла видимости записанного; максимумы длительности по прогонам
— запись 183 / 153 / 1301 / 316 / 959 / 190 / 538 / 2668 мс, чтение 241 / 146 / 894 / 314 / 145 / 163 / 507 / 603 мс.** Утверждать, что
записи не блокируются НИКОГДА, эти данные не позволяют: окно переключения
одного шарда МОГЛО оказаться короче секундного интервала опроса и не попасть
под пробу (длительность самого окна здесь не измерялась, поэтому «заведомо
короче» сказать нельзя).

Строки пишущей пробы удаляются в конце (и в `trap`), наполнение `orders`
проверяется явным гейтом. Во всех восьми прогонах набора (22/23/24/26/27/29/30/31) гейт
подтвердил возврат к исходным 4000 строкам. Без этого соседние артефакты,
считающие по 4000 заказов, начали бы давать другие числа.

**Осторожно с этими числами — их легко перепутать (проверено, спуталось).**
`get_rebalance_table_shards_plan()` вернула **30 СТРОК** (по 10 на каждую из
трёх распределённых таблиц), а Citus запланировал **20 ПЕРЕМЕЩЕНИЙ**
(`Scheduled 20 moves as job 30`). Это не противоречие: `customers` и `orders`
лежат в ОДНОЙ группе колокации (`colocation_id = 1`, проверено по
`citus_tables`), и парные шарды переезжают вместе, одним перемещением на
пару. Отсюда 10 перемещений на группу `customers`+`orders` (20 строк плана)
плюс 10 на `shipments` (своя группа, `colocation_id = 2`) = 20 перемещений
при 30 строках плана.

Строка плана ≠ перемещение. Писать «30 перемещений» НЕЛЬЗЯ.

Шардов `orders` в плане — 10: `102040, 102043, 102045, 102048, 102050,
102054, 102055, 102059, 102060, 102071`.

**⚠️ Обязательные оговорки.**

1. **Граница утверждения о доступности.** Наблюдений здесь
   **18–21 за прогон** (19 / 19 / 20 / 20 / 19 / 21 / 18 / 18 в прогонах
   22 / 23 / 24 / 26 / 27 / 29 / 30 / 31; в каноническом выводе выше — 18). Число проб зависит от того, сколько
   реально занял ребаланс на этом хосте, поэтому это диапазон, а не константа;
   более широкие цифры из более ранних записей сюда не годятся — те прогоны
   считали ещё и посттерминальную пробу и из набора исключены. Эти 18–21 проб
   покрывают ВЕСЬ ребаланс на игрушечном объёме (4000 заказов, ~20
   перемещений). Читать это следует как «в коротком прогоне на маленьких
   данных ни одна проба не отказала», а НЕ как гарантию доступности вообще.
   На реальных объёмах ребаланс идёт часами, перемещений тысячи, и окно, в
   котором что-то может пойти не так, несоизмеримо шире. Отдельно: большая
   выборка проб сама по себе не аргумент — в одном из ранних прогонов
   этого стенда набралось 1200 успешных проб, но лишь потому, что ребаланс
   тогда ЗАВИС (см. следующий пункт) и почти всё это время шарды не
   двигались; выборка измеряла в основном простой, а не устойчивость под
   нагрузкой переноса.
2. **НЕБЛОКИРУЮЩИЙ ребаланс (режим `auto`) требует `wal_level=logical`; в
   этом стенде он включён на ВСЕХ ЧЕТЫРЁХ СЕРВИСАХ compose.**
   `shard_transfer_mode='auto'` (значение по умолчанию) выбирает логическую
   репликацию для переноса — именно она позволяет избежать блокировки
   ЗАПИСЕЙ. ⚠️ Требование относится ТОЛЬКО к этому режиму: у `block_writes`
   перенос идёт через `COPY` с блокировкой ЗАПИСЕЙ, и `wal_level=logical` ему
   не нужен. Чтение доступно в обоих режимах — по чтению режимы не
   различаются. Стенд режим не менял, поэтому проверено только про `auto` —
   обобщать на «ребаланс вообще» нельзя.
   Образ `citusdata/citus` по умолчанию поднимается с `wal_level=replica`;
   с ним ребаланс не падает читаемой ошибкой, а ЗАВИСАЕТ: задача
   `replicate_reference_tables` уходит в ЗАТЯЖНОЙ ПОВТОР с «logical decoding
   requires wal_level >= logical» и блокирует собой все запланированные
   перемещения (воспроизведено при постановке стенда: 8 повторов,
   20 заблокированных задач, дольше 5 минут без прогресса). Наблюдение
   ПРЕРВАНО ВРУЧНУЮ — поэтому доказан затяжной повтор без прогресса, а НЕ
   бесконечный цикл: сколько бы он продолжался дальше, здесь не измерено.

   **Что именно проверено, а что нет.** Проверено: с `wal_level=replica`
   ВЕЗДЕ перенос не идёт; с `wal_level=logical` ВЕЗДЕ идёт. НЕ проверено:
   нужен ли `logical` именно на КООРДИНАТОРЕ. Эксперимент сравнивал только
   «везде `replica`» против «везде `logical`» — роль координатора в нём не
   изолирована, отдельного контрольного опыта (`logical` только на воркерах,
   на координаторе `replica`) не ставилось. Документация Citus описывает
   ребаланс как перенос между ВОРКЕРАМИ, а совет включать логическое
   декодирование на всех узлах дан для другого сценария — внешнего CDC.
   Это разрыв между измерением и выводом, и закрывается он этой оговоркой,
   а не догадкой: единая настройка на всех сервисах — сознательный выбор
   конфигурации стенда, а не доказанное требование к координатору.
   Воспроизводить находку заново не нужно, но при копировании конфигурации
   на другой стенд про неё важно помнить.
3. `pg_dist_placement.shardstate` может оставлять осиротевшие записи
   (`shardstate=4`) до уборки фоновым процессом — все запросы в скрипте
   фильтруют `WHERE shardstate = 1`.
4. **Длительность окна опроса: два прямых измерения — 31529 мс и 39928 мс.**
   Величина снимается часами (`POLL_START_MS` → `POLL_WALL_MS`) от первого
   опроса состояния до первого наблюдения `finished`. Первое значение — из
   прогона `job_id=30` (машина автора), второе — из `job_id=31`, снятого
   внешним ревью на другой машине (провенанс, как и у прочих сторонних
   прогонов, указывается явно). Для остальных прогонов набора длительность
   НЕ фиксировалась — тогда её ещё не измеряли.

   ⚠️ Второе измерение важно тем, что **отменило формулировку «укладывается
   в полминуты»**, которая держалась ровно один раунд: 39.9 с — это
   заметно больше полуминуты, да и 31.5 с её уже превышало. Корректно говорить «десятки
   секунд». Заодно `job_id=31` дал максимум записи **2668 мс** — больше
   любого в наборе, снова при нуле отказов и без потери видимости.
   ⚠️ Доводом ни за, ни против блокировки это НЕ является: успешный `INSERT`
   мог сначала ждать блокировку, а потом завершиться, и стенд эти случаи не
   различает (см. разбор выше). Измерение здесь неоднозначно, и только.
   Длительность окна — величина для игрушечного объёма стенда, на реальные объёмы не
   переносится ни по масштабу, ни по порядку. И это не время фактического
   переноса шардов — в окно входят ожидание планировщика, копирование
   справочника и паузы между moves.

   ⚠️ **Прежняя цифра «~21–26 с» была ИНФЕРЕНЦИЕЙ, а не замером, и она
   опровергнута.** Её выводили из числа итераций (19–21 проба ≈ 19–21 с плюс
   накладные), то есть считали, а не мерили; в FIXTURES она стояла как
   «фактическая длительность по наблюдавшимся прогонам». Первое же прямое
   измерение дало 31529 мс — примерно на треть больше верхней границы
   выведенного диапазона. Цифра «21–26 с» удалена отовсюду; цитировать её
   нельзя. Урок тот же, что и с «типичной записью 116–130 мс»: величина,
   полученная арифметикой из другой величины, не становится от этого
   измерением.
5. **`citus_drain_node` не трогает копии референсных таблиц — это
   установленный факт, а не разовое наблюдение одного прогона.** Слив узла
   переносит только размещения РАСПРЕДЕЛЁННЫХ таблиц; копия референсной
   `carriers` физически остаётся на осушаемом узле до `citus_remove_node`,
   который снимает узел вместе с её последним размещением. Раньше это было
   записано в FIXTURES как «наблюдение без установленной причины» (лог
   показывал промежуточное «осталось размещений: 1» и не объяснял, что это
   такое) — причина найдена: это копия `carriers`, и `citus_drain_node` её
   принципиально не трогает. Следствие обнаружилось при проверке этой
   находки и было устранено отдельно: старая версия цикла ожидания в
   `scripts/rebalance-demo.sh` ждала нуля ВСЕХ размещений на `citus-w3`
   (включая референсные), а ноль был структурно НЕДОСТИЖИМ, пока копия
   `carriers` жива на узле, — цикл выжигал полные 60 попыток × 5 с = **300
   секунд впустую на КАЖДОМ штатном прогоне** этого артефакта, хотя
   реальное опустошение от распределённых шардов занимает секунды. Скрипт
   исправлен: цикл ждёт опустошения только от размещений распределённых
   таблиц, референсная копия дожидается `citus_remove_node`. Проверено
   дважды подряд после исправления: прогон занял 72–74 с вместо прежних
   (при срабатывании этого пути) плюс пяти минут, `pg_dist_cleanup`
   вернулся к 0 в обоих контрольных прогонах.
6. **Отложенная запись `pg_dist_cleanup` требует явной уборки, пока узел ещё
   жив (находка при проверке предыдущего пункта, не из исходного ревью).**
   `citus_drain_node` оставляет в `pg_dist_cleanup` отложенную (`policy_type`
   = DEFERRED) запись на удаление старой копии перенесённого шарда на
   осушённом узле — сам перенос эту запись не убирает. Проверено на **Citus
   14.1** в **этом конкретном сценарии** (drain → remove → удаление
   контейнера): если снять регистрацию узла (`citus_remove_node`) и
   уничтожить его контейнер ДО того, как запись обработана, она остаётся
   неразобранной — `node_group_id` в ней указывает на уже несуществующий
   узел, и явный вызов `CALL citus_cleanup_orphaned_resources();` ПОСЛЕ
   удаления узла её не разобрал: `pg_dist_cleanup` оставался = 1. Сработал
   бы какой-то иной путь уборки (перезапуск координатора, ручное удаление
   строки, более поздняя версия Citus) — здесь НЕ проверялось, поэтому
   «навсегда» не утверждается. Рабочее окно, которое здесь проверено, —
   вызвать `CALL citus_cleanup_orphaned_resources();` ПОСЛЕ
   `citus_drain_node`, но ДО `citus_remove_node`, пока `citus-w3` ещё
   зарегистрирован и его контейнер жив; о том, что оно единственно
   возможное, данных нет. `scripts/rebalance-demo.sh` исправлен соответственно, и в
   самопроверку добавлен явный гейт на `pg_dist_cleanup = 0` после уборки —
   раньше это нигде не проверялось скриптом автоматически, только
   отчётом человека постфактум.
7. **Связка отказов при ручной остановке ребаланса (задокументированный
   факт Task 6, не переисполняется в штатном прогоне этого скрипта — сам
   скрипт устроен так, чтобы в неё не попадать).** Если вызвать
   `citus_remove_node` сразу за `citus_drain_node`, снятие узла отказывает:
   слив ставит ФОНОВУЮ задачу и возвращает управление немедленно, шарды
   ещё физически на узле. Хуже: сам слив помечает узел
   `shouldhaveshards=false`, и если в этот момент жив ДРУГОЙ, ранее
   запущенный ребаланс, планировавший перенос шардов НА этот узел, его
   задачи начинают падать с `ERROR: Moving shards to a node that
   shouldn't have a shard is not supported` — **наблюдалось 11 повторов за
   семь минут** в состоянии `running`, без единого сдвинутого шарда;
   наблюдение прервано вручную, поэтому сколько повторов задача сделала бы
   дальше и остановилась бы она сама — не проверялось. Лечится
   `citus_job_cancel(job_id)` (отменить зависшую задачу) перед повторным
   сливом. Именно поэтому уборка `rebalance-demo.sh` сначала гасит
   незавершённые фоновые задачи, затем сливает узел и ЖДЁТ фактического
   опустошения, и только потом снимает регистрацию — короткий путь «слить
   → сразу снять» ломается этой же связкой.
8. **Сравнение с обычным (нешардированным) PostgreSQL по скорости здесь
   запрещено** — см. глобальную оговорку в начале файла. Артефакт вообще
   не про производительность запросов, а про механику ребаланса; ванильный
   PostgreSQL не масштабируется на несколько узлов и сравнивать здесь
   попросту нечего.

## Состояние стенда после прогона

Проверено сразу после всех пяти прогонов, независимо от вывода скриптов:

```
$ SELECT nodeid, nodename, isactive, groupid FROM pg_dist_node ORDER BY nodeid;
 nodeid | nodename | isactive | groupid
--------+----------+----------+---------
      1 | citus-w1 | t        |       1
      2 | citus-w2 | t        |       2
(2 rows)

$ SELECT count(*) FROM pg_dist_cleanup;
0

$ SELECT table_name, citus_table_type, shard_count FROM citus_tables ORDER BY table_name;
 table_name | citus_table_type | shard_count
------------+------------------+-------------
 customers  | distributed      |          32
 orders     | distributed      |          32
 shipments  | distributed      |          32
 carriers   | reference        |           1
(4 rows)

$ docker inspect citus-w3   # (после команды exit-код ненулевой)
Error: No such object: citus-w3
```

Кластер вернулся к исходным двум воркерам, лишних записей в
`pg_dist_cleanup` нет, третий узел не остался, схема (4 таблицы) не
изменилась. Стенд в состоянии, пригодном для промоута.
