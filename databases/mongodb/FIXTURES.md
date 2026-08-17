# FIXTURES — реальные числа стендов MongoDB cookbook (источник для Phase 2)

Сводка живых чисел, снятых на реальных кластерах (`mongo:8.2.11` replica set
и sharded-топология, `postgres:18` контраст, docker compose, Docker
Desktop/Windows) в ходе Task 1–9 этого cookbook. Каждая строка — **реально
измеренное** значение, ссылка на раздел `README.md`, где оно снято и
объяснено подробнее. Ничего здесь не придумывается и не пересчитывается —
только переносится из уже закоммиченного `mongodb/README.md` (и, где
отмечено, `scratchout/*.txt`, не коммитится, воспроизводимо по инструкциям
README) после реального прогона соответствующего стенда.

Формат: `утверждение → число (единица) → источник`.

Датасет и версии — сквозные для всех стендов, выносятся в отдельный раздел
внизу вместе с честными оговорками, общими для нескольких стендов (по
образцу `../scylladb/FIXTURES.md`).

Секции ниже заполняются по мере прохождения задач серии — на Task 1 (этот
коммит) все пустые: топология стендов есть, живых прогонов ещё не было.

---

## Стенд #1 — документная модель и схема-дизайн (modeling)

Живой прогон `bash ops/modeling-demo.sh` (Task 3) — rs0 (3 узла, `mongo:8.2.11`)
+ `postgres:18`, полный датасет (`seed=42`: users=50000, products=5000,
orders=200000, см. `dataset/manifest.json`), `mongoimport` + `psql -f
pg-seed.sql`. FIXTURE-строки дословно (Go stdout стенда `modeling/main.go`):

```
FIXTURE modeling: import_users=50000 import_products=5000 import_orders=200000
FIXTURE modeling: orders_ref_build_latency=1.444103462s orders_ref_count=200000
FIXTURE modeling: sample_orders=500 embed_round_trips=500 embed_latency=123.602959ms ref_naive_round_trips=2019 ref_naive_latency=477.317971ms ref_naive_items=1519 ref_lookup_round_trips=2 ref_lookup_aggregate_calls=1 ref_lookup_getmore_calls=1 ref_lookup_latency=18.705411ms ref_lookup_orders_returned=500
FIXTURE modeling: growth_batches_ok=82 growth_items_ok=8200 growth_last_size_bytes=16653130 growth_hit_limit=true growth_limit_err="write exception: write errors: [Plan executor error during update :: caused by :: Resulting document after update is larger than 16777216]" growth_latency_first10pct=9.04077ms growth_latency_last10pct=40.172358ms
FIXTURE modeling: pg_jsonb_avg_size_bytes=575 mongo_bson_avg_size_bytes=380 pg_key_read_avg_latency=175.603µs mongo_key_read_avg_latency=264.653µs pg_reads_ok=500 mongo_reads_ok=500 sample=500
```

**embed vs reference, «заказ + его позиции» (случайная выборка 500 заказов,
`$sample`):**

| Путь | round-trips (всего на выборку) | латентность (всего на выборку) |
|---|---|---|
| embedded (`orders`, items денормализованы) | **500** (1/заказ) | 123.6 ms |
| referenced, наивно (`orders_ref` + по `FindOne` на `products` за каждый item) | **2019** (1 + N/заказ, N=1519 items) | 477.3 ms |
| referenced, `$lookup` (1 агрегатный вызов на ВСЮ выборку сразу) | **2** (1 aggregate + 1 getMore) | 18.7 ms |

Ассерт (прошёл): `embed_round_trips(500) < ref_naive_round_trips(2019)` и
`ref_lookup_round_trips(2) < ref_naive_round_trips(2019)`.

**Честная оговорка:** `$lookup`-путь — это ОДИН агрегатный вызов на ВСЮ
выборку (`$match {_id:{$in:[...]}}` + `$lookup`), но не «1 round-trip на всю
выборку»: `ref_lookup_round_trips` теперь РЕАЛЬНО измерено через
`*event.CommandMonitor` (счёт `Started`-событий `aggregate`/`getMore` на
выделенном клиенте, обслуживающем только этот сценарий) — при вычерпывании
курсора `cur.All` на 500 заказах драйвер делает **1 `aggregate` + 1
`getMore` = 2 сетевых round-trip** (дефолтный первый батч ~101 документ,
остаток добирается одним `getMore`). Раньше здесь стояла захардкоженная
единица, не учитывавшая `getMore` — теперь число измеренное. Числа
`ref_lookup_*` всё равно НЕ apples-to-apples с `embed_*`/`ref_naive_*` (те
считаются на заказ, а `$lookup` — на ВСЮ выборку сразу). Педагогический
вывод не «embedded всегда меньше round-trips, чем referenced» буквально — он
верен именно против НАИВНОГО referenced-паттерна (per-item подтягивание в
коде приложения, без `$lookup`), что и типичный анти-паттерн, который
embedding решает. `$lookup` сам по себе тоже сводит referenced-чтение к
единицам round-trip (2 против 2019 у наивного пути — тот же порядок, что и
embedded), но перекладывает N join'ов на сервер — не устраняет их.

**Рост документа (WiredTiger, `growth_demo`, $push $each пачками по 100
элементов ~2000 байт полезной нагрузки):**

- 82 успешных батча (8200 элементов), размер строго рос **202930 → 16653130
  байт**, затем **сервер отказал** на батче 83 с ошибкой `Resulting document
  after update is larger than 16777216` (жёсткий лимит BSON-документа
  16 МиБ = 16777216 байт, упёрлись ровно в него).
- Латентность на батч росла вместе с размером документа: среднее по первым
  10% успешных батчей — **9.0 ms**, по последним 10% — **40.2 ms** (~4.4×).
- **Расхождение с ожиданием брифа:** WiredTiger (в отличие от старого
  MMAPv1) НЕ «перемещает» документ при превышении выделенного места — у
  него нет padding/фиксированных слотов, каждое обновление — это
  read-modify-write всего документа в B-дереве (MVCC), поэтому никакого
  отдельного «move»-события снаружи не наблюдаемо. Реально наблюдаемое
  поведение роста — (1) латентность на батч растёт вместе с размером
  документа (не скачком, а плавно, что согласуется с «весь документ
  перезаписывается каждый раз»), и (2) рост монотонно продолжается ровно до
  жёсткого лимита BSON-документа (16 МиБ), после чего сервер твёрдо
  отказывает записи — это и есть реальный, наблюдаемый потолок embedding
  неограниченно растущих массивов, а не деградация производительности из-за
  «перемещения записи».
- Ассерт (прошёл): минимум несколько успешных батчей + размер строго
  возрастает от батча к батчу (`sort.SliceIsSorted`).

**PG jsonb-контраст (тот же документ заказа, выборка 500, по одному запросу
на заказ в обеих системах):**

| Метрика | Mongo (`orders`, BSON, `$bsonSize`) | PostgreSQL (`orders_doc.doc`, jsonb, `pg_column_size`) |
|---|---|---|
| средний размер документа | **380 байт** | **575 байт** |
| латентность чтения одного поля (`status`) по `_id`, среднее на выборке | **264.7 µs** | **175.6 µs** |

Ассерт (прошёл): все 500/500 чтений успешны в обеих системах.

**Честная оговорка (размер):** `$bsonSize` — логический размер BSON-
представления документа (без учёта сжатия/оверхеда хранения WiredTiger);
`pg_column_size` — реальный размер СОХРАНЁННОГО значения колонки jsonb на
диске (может включать TOAST-оверхед и/или сжатие PostgreSQL). Числа не
идентичны по методологии измерения — оба реальны и взяты из живых систем,
но jsonb в PG на этой выборке оказался **больше** BSON в среднем (575 vs 380
байт), что противоречит наивному «документ = документ» и стоит явно
проговорить в статье как разные единицы измерения, а не как факт «PG
компактнее/больше Mongo».

**Честная оговорка (латентность key-read):** на этом стенде (docker-compose
на одной машине, без сетевой задержки между контейнерами) PG оказался
БЫСТРЕЕ на единичном чтении по ключу (175.6 µs vs 264.7 µs) — контраст со
«здравым смыслом», что документная БД быстрее для документного доступа;
разница здесь, вероятнее всего, объясняется тем, что PG single-row lookup
по PK — предельно оптимизированный путь, а Mongo `FindOne` идёт через
полный wire-протокол/BSON-декодирование клиентского драйвера v2; для статьи
эта цифра — честный «не всё так просто», не аргумент «Postgres быстрее
Mongo» в общем случае (единичный прогон на одной машине, без прогрева/
повторов/p95-p99, без индекса на `_id` иного порядка — оба используют PK/
`_id`-индекс).

Импорт: 50000/5000/200000 — совпадает с `dataset/manifest.json` (assert
прошёл). Датасет и версии образов — см. «Сквозные факты» ниже.

## Стенд #2 — WiredTiger: движок хранения

Живой прогон `bash ops/wiredtiger-demo.sh` (Task 4) — rs0 (3 узла,
`mongo:8.2.11`), полный датасет (`seed=42`: users=50000, products=5000,
orders=200000, см. `dataset/manifest.json`), `mongoimport`. Без PostgreSQL —
контраст с PG WAL остаётся граничной ссылкой в тексте статьи, не отдельным
измерением. FIXTURE-строки дословно (Go stdout стенда `wiredtiger/main.go`):

```
FIXTURE wiredtiger: import_users=50000 import_products=5000 import_orders=200000
FIXTURE wiredtiger: phase=before bytes_in_cache=292397591 dirty_bytes=292365910 modified_evicted=33 unmodified_evicted=0 pages_requested=521039 log_bytes_written=34064256 checkpoints_succeed=4
FIXTURE wiredtiger: phase=after_read_load bytes_in_cache=292397591 dirty_bytes=292365910 modified_evicted=33 unmodified_evicted=0 pages_requested=521062 log_bytes_written=34064256 checkpoints_succeed=4
FIXTURE wiredtiger: read_load_docs=200000 read_load_latency=323.486838ms
FIXTURE wiredtiger: phase=after_write_load bytes_in_cache=507145699 dirty_bytes=507114017 modified_evicted=57 unmodified_evicted=0 pages_requested=725253 log_bytes_written=40548864 checkpoints_succeed=4
FIXTURE wiredtiger: write_load_docs=100000 write_load_payload_bytes=800 write_load_latency=946.328636ms
FIXTURE wiredtiger: phase=after_checkpoint bytes_in_cache=507524147 dirty_bytes=0 modified_evicted=57 unmodified_evicted=0 pages_requested=727126 log_bytes_written=40556160 checkpoints_succeed=5
FIXTURE wiredtiger: fsync_latency=689.971877ms
```

**Сценарий:** снимок `db.serverStatus().wiredTiger` (секции `cache`,
`log`, `checkpoint`) ДО любой нагрузки этого стенда → read-нагрузка (полный
скан 200000 документов `orders`, проекция `_id`+`status`) → снимок → write-
нагрузка (bulk-вставка 100000 документов ~800 байт payload каждый в
отдельную коллекцию `wt_load`) → снимок → принудительный checkpoint
(`db.adminCommand({fsync:1})`) → снимок. Каждый снимок явно снят с primary
(`readpref.Primary()`) — WiredTiger cache/checkpoint/log это ресурс
КОНКРЕТНОГО узла, не реплицируется, и вся нагрузка стенда тоже идёт на
primary.

| Метрика | before | after_read_load | after_write_load | after_checkpoint |
|---|---|---|---|---|
| `bytes currently in the cache` | 292 397 591 | 292 397 591 | 507 145 699 | 507 524 147 |
| `tracked dirty bytes in the cache` | 292 365 910 | 292 365 910 | **507 114 017** | **0** |
| `pages requested from the cache` | 521 039 | 521 062 | 725 253 | 727 126 |
| `log bytes written` | 34 064 256 | 34 064 256 | **40 548 864** | 40 556 160 |
| `total succeed number of checkpoints` | 4 | 4 | 4 | **5** |

**Ассерты (все прошли, живой прогон):**

- read-нагрузка (200000/200000 документов `orders`, совпадает с
  `manifest.json`) подняла `pages requested from the cache` (521039 →
  521062) — чтение ВСЕГДА запрашивает страницы у кеша, даже когда кеш уже
  тёплый после `mongoimport` (никакого прироста `bytes currently in the
  cache`/`dirty bytes` от read-нагрузки не было — read-only обращения не
  создают dirty-страниц, только заново проходят через кеш).
- write-нагрузка (100000 документов, ~800 байт payload каждый) подняла
  `tracked dirty bytes in the cache` (292 365 910 → **507 114 017**, ×1.7) и
  журнал `log bytes written` (34 064 256 → **40 548 864**, +6 484 608 байт)
  — журнал растёт при записи ДО какого-либо checkpoint (в этом же батче
  записи, без принудительного `fsync`).
- принудительный checkpoint (`fsync:1`) поднял `total succeed number of
  checkpoints` (4 → **5**) и уронил `tracked dirty bytes in the cache` до
  **0** (507 114 017 → 0) — checkpoint сбросил ВСЕ dirty-страницы на диск
  разом; `bytes currently in the cache` при этом почти не изменился (507.1M
  → 507.5M) — checkpoint не выселяет страницы из кеша, только помечает их
  чистыми (clean) на диске, сам объём резидентных в кеше страниц остаётся
  тем же.

**Расхождение с наивным ожиданием — эвикция (`modified_evicted`/
`unmodified_evicted`):** ни read-, ни write-нагрузка НЕ вызвали эвикции
страниц из кеша (счётчики `modified pages evicted`/`unmodified pages
evicted` не выросли за read-нагрузку, `modified_evicted` вырос только с 33
до 57 за ВСЮ write-нагрузку — на 24 страницы, несопоставимо мало с объёмом
записанных данных). Причина: WiredTiger запускает активную эвикцию только
когда занятость кеша данными переваливает за настроенные пороги (по
умолчанию — eviction target/trigger в процентах от `cache_size`), а
100 000 документов ×~800 байт (≈100 МБ payload, реально в кеше выросло на
~215 МБ с учётом BSON-оверхеда и служебных структур B-дерева) — на этом
хосте (docker compose, дефолтный `cache_size` WiredTiger от свободной
памяти контейнера/хоста) не хватило, чтобы всерьёз задеть эти пороги за
один прогон. Значит на бо́льшем объёме нагрузки (или меньшем `cache_size`)
эвикция станет заметной — этот стенд её просто не спровоцировал, что само
по себе честный, наблюдаемый факт, а не баг ассерта (эвикция намеренно НЕ
входит в assert-инварианты этого стенда, только записана как факт).

**В отличие от Task 3 (`growth_demo`, MMAPv1-мифа про "перемещение
документа")** — здесь наблюдаемое поведение полностью совпало с ожиданием
брифа: dirty bytes растут под записью и падают до нуля после checkpoint,
журнал растёт при записи независимо от checkpoint. Расхождений с ожиданием
не было (кроме эвикции — см. выше, отдельная, не входящая в assert-набор
метрика).

Импорт: 50000/5000/200000 — совпадает с `dataset/manifest.json` (assert
прошёл). Датасет и версии образов — см. «Сквозные факты» ниже.

## Стенд #3 — индексы вглубь (multikey, ESR, explain)

Живой прогон `bash ops/indexes-demo.sh` (Task 5) — rs0 (3 узла, `mongo:8.2.11`),
полный датасет (`seed=42`: users=50000, products=5000, orders=200000, см.
`dataset/manifest.json`), `mongoimport`. Все explain-планы — реальные,
`db.RunCommand({explain: {...}, verbosity: "executionStats"})` через
`Database.RunCommand` (у mongo-go-driver/v2 нет высокоуровневого
`Collection.Explain()`). FIXTURE-строки дословно (Go stdout стенда
`indexes/main.go`, финальный прогон):

```
FIXTURE indexes: import_users=50000 import_products=5000 import_orders=200000
FIXTURE indexes: multikey_filter=tags:vip index=idx_tags_multikey ixscan_found=true is_multikey=true collscan_present=false n_returned=8847 keys_examined=8847 docs_examined=8847
FIXTURE indexes: esr_variant=correct index=idx_esr_correct order=E,S,R(status,created_at,total) sort_stage=false collscan=false n_returned=28759 keys_examined=33252 docs_examined=28759
FIXTURE indexes: esr_variant=wrong index=idx_esr_wrong order=E,R,S(status,total,created_at) sort_stage=true collscan=false n_returned=28759 keys_examined=28759 docs_examined=28759
FIXTURE indexes: partial_query=compatible(total>1500) hinted_ixscan_found=true index=idx_total_partial n_returned=113550 keys_examined=113550 docs_examined=113550
FIXTURE indexes: partial_query=incompatible(total>500) natural_ixscan_found=false natural_index= natural_collscan=true natural_n_returned=172957
FIXTURE indexes: partial_query=incompatible(total>500) forced_hint_error="<none>"
FIXTURE indexes: partial_query=incompatible(total>500) forced_hint_plan_index=idx_total_partial forced_hint_collscan=false forced_hint_n_returned=143896 forced_hint_docs_examined=143896 natural_n_returned_for_comparison=172957
FIXTURE indexes: ttl_index=idx_ttl_expires_at ttl_past_doc_expired=true ttl_wait=40s ttl_future_doc_present=true max_wait=2m0s
FIXTURE indexes: covered_query_filter=category:electronics projection_covered=true fetch_present=false total_docs_examined=0 total_keys_examined=340 n_returned=340
```

### Multikey — `users.tags` (`idx_tags_multikey`, запрос `{tags: "vip"}`)

| Метрика | Значение |
|---|---|
| план | `IXSCAN(idx_tags_multikey)`, `isMultiKey=true`, `COLLSCAN` отсутствует |
| `totalKeysExamined` | **8847** |
| `totalDocsExamined` | **8847** |
| `nReturned` | **8847** |

Ассерт (прошёл): `IXSCAN` найден, `COLLSCAN` отсутствует, индекс `isMultiKey=true`.
`keysExamined == docsExamined == nReturned` — равенство ожидаемое: у
конкретного пользователя тег `"vip"` встречается максимум один раз в массиве
(теги — случайная перестановка без повторов, `dataset/main.go` `genUsers`),
поэтому один ключ индекса на пользователя, дублей для дедупликации нет.

### ESR (Equality→Sort→Range) — **основной payoff стенда**

Один и тот же запрос (`status="paid"` equality + `total>500` range,
`sort({created_at: 1})`) форсируется через `.hint()` на два compound-индекса
с ОДНИМИ и теми же тремя полями в разном порядке:

| Индекс | Порядок полей | `SORT`-стадия | `COLLSCAN` | `keysExamined` | `docsExamined` | `nReturned` |
|---|---|---|---|---|---|---|
| `idx_esr_correct` | E,S,R = `status,created_at,total` | **отсутствует** | нет | 33252 | 28759 | 28759 |
| `idx_esr_wrong` | E,R,S = `status,total,created_at` | **присутствует** | нет | 28759 | 28759 | 28759 |

Ассерт (прошёл, живой контраст на ОДНОМ запросе): ESR-верный индекс
(`status,created_at,total`) даёт план БЕЗ blocking `SORT`; ESR-неверный
(`status,total,created_at` — range-поле перед sort-полем) — план С `SORT`.
`nReturned` совпадает для обоих (28759) — оба индекса ищут один и тот же
результат, отличается только план его получения.

**Честная оговорка (нюанс, не предполагавшийся заранее):** ESR-верный
индекс избегает `SORT`, но при этом просматривает **БОЛЬШЕ** ключей индекса,
чем ESR-неверный (33252 против 28759, +15.6%) — `keysExamined >
docsExamined` только у `idx_esr_correct` (33252 vs 28759, разница 4493).
Причина: `total` (range-поле) стоит ПОСЛЕДНИМ полем индекса, ПОСЛЕ
sort-поля `created_at` — чтобы сохранить порядок по `created_at`, сервер
не может построить настолько же плотные bounds по `total`, как в индексе,
где `total` идёт сразу после equality-поля (`idx_esr_wrong`): часть
просмотренных ключей отбрасывается фильтром внутри `IXSCAN`, не долетая до
`FETCH`. Это классический, документированный компромисс ESR: индекс,
исключающий blocking in-memory `SORT`, может просматривать больше записей
индекса, чем идеально узкий (equality+range без sort) индекс — но избегает
именно `SORT` (который в худшем случае требует буферизации ВСЕГО
результата в памяти/на диске и не масштабируется на больших выборках, в
отличие от чуть более широкого, но потокового `IXSCAN`).

### Partial index — `orders.total`, `partialFilterExpression: {total: {$gt: 1000}}`

| Запрос | Естественный план (без hint) | Forced hint на `idx_total_partial` | `nReturned` |
|---|---|---|---|
| `total>1500` (⊆ `total>1000`, совместим) | — | `IXSCAN(idx_total_partial)` | **113550** |
| `total>500` (пересекается, НЕ подмножество `total>1000`) | `COLLSCAN`, индекс НЕ выбран | `IXSCAN(idx_total_partial)`, БЕЗ ошибки | **143896** (форсированно) vs **172957** (корректный полный скан) |

Ассерты (прошли):
- запрос-подмножество (`total>1500`) через forced hint реально использует
  `idx_total_partial` (113550/113550/113550 keys/docs/nReturned);
- запрос ВНЕ partial-фильтра (`total>500`), **без hint**, планировщик сам
  НЕ выбирает `idx_total_partial` (естественный план — `COLLSCAN`,
  172957 документов) — это и есть наблюдаемый эффект partial-индекса:
  оптимизатор исключает его из кандидатов, когда не может доказать, что
  предикат запроса — подмножество `partialFilterExpression`.

**ВАЖНОЕ расхождение с наивным ожиданием (не входит в assert-набор этого
стенда, но принципиально для статьи):** форсированный `.hint("idx_total_partial")`
на запрос `total>500` (НЕ подмножество partial-фильтра) **explain'ится БЕЗ
ошибки** — сервер НЕ отклоняет несовместимый forced hint. При этом
`winningPlan` реально ссылается на `idx_total_partial`, и запрос возвращает
**143896** документов — МЕНЬШЕ, чем корректный результат полного скана
(**172957**). Разница — 29061 документ с `500 < total <= 1000`, которых
физически нет в partial-индексе (он не содержит записей вне
`partialFilterExpression`) и которые **молча теряются** при форсированном
hint, вместо ошибки или предупреждения. Это задокументированное, но
контринтуитивное поведение MongoDB: `.hint()` на partial-индекс — это
контракт "я как разработчик ручаюсь, что предикат совместим с
partial-фильтром"; при нарушении этого контракта сервер не проверяет
совместимость и тихо возвращает неполный результат. Практический вывод для
статьи: `.hint()` на partial-индекс безопасен только тогда, когда
предикат запроса гарантированно — подмножество `partialFilterExpression`
(в идеале программно тот же самый фильтр); без hint планировщик ведёт себя
безопасно (просто не рассматривает индекс-кандидат), с hint — ответственность
на разработчике.

### TTL index — `ttl_demo.expires_at`, `expireAfterSeconds: 0`

| Документ | `expires_at` относительно вставки | Результат |
|---|---|---|
| `ttl-past` | −1 час (уже в прошлом) | удалён фоновым TTL monitor'ом за **40s** |
| `ttl-future` | +1 час (в будущем) | остался нетронутым за то же окно |

Ассерты (прошли): просроченный документ реально удалён (poll каждые 10s, до
120s верхней границы — удаление зафиксировано на 4-м опросе, 40s); документ
с будущим `expires_at` не тронут за то же окно ожидания. Совпадает с
ожиданием брифа: TTL monitor по умолчанию проходит раз в ~60s, реальное
удаление на этом прогоне уложилось в 40s (не гарантированное число — зависит
от фазы монитора относительно момента вставки, честно фиксируется КАЖДЫЙ
прогон, а не константа).

### Covered query — `products` (`idx_category_price_covering: {category:1, price:1}`)

Запрос `{category: "electronics"}` с проекцией `{category:1, price:1,
_id:0}` (подмножество ключей индекса, `_id` явно исключён):

| Метрика | Значение |
|---|---|
| `totalDocsExamined` | **0** |
| `totalKeysExamined` | 340 |
| `nReturned` | 340 |
| план | `PROJECTION_COVERED`, `FETCH` отсутствует |

Ассерт (прошёл): `totalDocsExamined=0`, `PROJECTION_COVERED` присутствует в
`winningPlan`, `FETCH`-стадия отсутствует — запрос полностью обслужен из
индекса, ни один документ не был прочитан.

Импорт: 50000/5000/200000 — совпадает с `dataset/manifest.json` (assert
прошёл). Датасет и версии образов — см. «Сквозные факты» ниже.

## Стенд #4 — aggregation pipeline вглубь

Живой прогон `bash ops/aggregation-demo.sh` (Task 6) — rs0 (3 узла,
`mongo:8.2.11`), полный датасет (`seed=42`: users=50000, products=5000,
orders=200000, см. `dataset/manifest.json`). ДВА клиента на ОДНОМ и том же
живом кластере с ОДНИМ и тем же пайплайном — Go (`aggregation/main.go`,
mongo-go-driver/v2) и Java-зеркало (`java/aggregation/`,
`org.mongodb:mongodb-driver-sync` 5.5.1, первый модуль Java-реактора
`java/`, устанавливаемого этой задачей). FIXTURE-строки дословно:

```
FIXTURE aggregation: import_users=50000 import_products=5000 import_orders=200000
FIXTURE aggregation: pipeline_match_status=paid index=idx_status_agg ixscan_found=true collscan_present=false group_merged_into_cursor=true n_returned_groups=24200 keys_examined=33251 docs_examined=33251 sort_used_disk=false sort_spills=0 wall_latency=128.184165ms result_groups=24200 sum_order_count=33251
FIXTURE aggregation: lookup_docs_examined=600462 lookup_keys_examined=600462 lookup_collection_scans=0 lookup_n_returned=600461 lookup_indexes_used=[_id_] lookup_stage_time_estimate=12661ms lookup_unwind_merged_into_lookup=true lookup_wall_latency=12.806006469s lookup_groups=15 nolookup_wall_latency=411.818923ms nolookup_groups=300 cost_ratio=31.1x
FIXTURE aggregation: unwind_group_distinct_products=5000 unwind_group_lines=600461 unwind_group_sum_revenue=376779509.73 cross_check_sum_orders_total=376779509.73 diff=0.0000 wall_latency=462.019505ms
FIXTURE aggregation: memory_limit_no_diskuse_error="(QueryExceededMemoryLimitNoDiskUseAllowed) ... Sort exceeded memory limit of 104857600 bytes, but did not opt in to external sorting." memory_limit_no_diskuse_code=292 memory_limit_no_diskuse_latency=167.050795ms
FIXTURE aggregation: memory_limit_with_diskuse_success=true memory_limit_with_diskuse_rows=600461 memory_limit_with_diskuse_latency=3.309409065s expected_rows=600461
```

Java-зеркало (тот же пайплайн, тот же живой кластер, сразу после Go в
рамках одного прогона `aggregation-demo.sh`):

```
FIXTURE aggregation-java: import_users=50000 import_products=5000 import_orders=200000
FIXTURE aggregation-java: pipeline_match_status=paid index=idx_status_agg ixscan_found=true collscan_present=false group_merged_into_cursor=true n_returned_groups=24200 keys_examined=33251 docs_examined=33251 sort_used_disk=false sort_spills=0 wall_latency=PT0.196658206S result_groups=24200 sum_order_count=33251
FIXTURE aggregation-java: lookup_docs_examined=600462 lookup_keys_examined=600462 lookup_collection_scans=0 lookup_n_returned=600461 lookup_indexes_used=[_id_] lookup_stage_time_estimate=17449ms lookup_unwind_merged_into_lookup=true lookup_wall_latency=PT13.145313303S lookup_groups=15 nolookup_wall_latency=PT0.432912214S nolookup_groups=300 cost_ratio=30.4x
FIXTURE aggregation-java: unwind_group_distinct_products=5000 unwind_group_lines=600461 unwind_group_sum_revenue=376779509.73 cross_check_sum_orders_total=376779509.73 diff=0.0000 wall_latency=PT0.49204645S
FIXTURE aggregation-java: memory_limit_no_diskuse_error="Command failed with error 292 (QueryExceededMemoryLimitNoDiskUseAllowed): ... Sort exceeded memory limit of 104857600 bytes, but did not opt in to external sorting." memory_limit_no_diskuse_code=292 memory_limit_no_diskuse_latency=PT0.184094495S
FIXTURE aggregation-java: memory_limit_with_diskuse_success=true memory_limit_with_diskuse_rows=600461 memory_limit_with_diskuse_latency=PT4.158478327S expected_rows=600461
```

**1. `$match->$group->$sort` (orders, status="paid"):** индекс
`idx_status_agg` на `orders.status`. Explain aggregate (executionStats)
показывает, что оптимизатор СЛИЛ `$match`+`$group` в единый SBE-план
внутри ОДНОЙ записи `"$cursor"` (IXSCAN(idx_status_agg) → FETCH → GROUP),
`$sort` остаётся отдельной поздней стадией (24200 групп — маленький объём,
`usedDisk=false spills=0`).

| | Go | Java |
|---|---|---|
| groups (users с paid-заказами) | 24200 | 24200 |
| keysExamined = docsExamined | 33251 | 33251 |
| index | `idx_status_agg` (IXSCAN, без COLLSCAN) | тот же |
| wall-latency | 128 ms | 197 ms |

Ассерты (прошли на обоих клиентах): IXSCAN найден и это `idx_status_agg`,
COLLSCAN отсутствует, `keysExamined==docsExamined` (равенство-фильтр по
единственному индексному полю), `totalDocsExamined` explain'а совпадает с
суммой `orderCount` по фактическому результату (те же paid-заказы,
посчитанные двумя независимыми путями).

**2. `$lookup` как join (orders×products) vs эквивалент без join:**
`$unwind items` → `$lookup(products, items.product_id, _id)` →
`$unwind product_doc` → `$group(category)` — против ТОЙ ЖЕ по объёму
работы агрегации `$unwind items` → `$group(items.product_name)` БЕЗ join.

| | Go | Java |
|---|---|---|
| `$lookup` totalDocsExamined | 600462 | 600462 |
| `$lookup` collectionScans | 0 (индекс `_id_`) | 0 (индекс `_id_`) |
| с `$lookup`, wall-latency | 12.81 s | 13.15 s |
| без `$lookup` (эквивалент), wall-latency | 412 ms | 433 ms |
| во сколько раз дороже | **31.1×** | **30.4×** |

Реальная стоимость `$lookup` записана как факт: индексированный (`_id_`,
`collectionScans=0` — НЕ плохой join со сканом), но всё равно **~31×**
дороже эквивалентной агрегации без join на тех же ~600К unwound-строках —
цена nested-loop join по одному документу на каждую unwound-строку, даже
при попадании в индекс каждый раз. Честная оговорка: следующий за
`$lookup` `$unwind` СЛИЛСЯ ВНУТРЬ самой `$lookup`-стадии (поле
`"unwinding"` в explain) — оптимизатор объединяет их в одну физическую
операцию, отдельной записи `"$unwind"` после `$lookup` в `stages` нет.

Ассерты (прошли на обоих клиентах): `totalDocsExamined>0` (join реально
выполнялся), `collectionScans==0` (индекс, не плохой план),
`$lookup`-путь строго дороже эквивалента без join.

**3. `$unwind`+`$group` по `items[]` (revenue/qty по product_id):**

| | Go | Java |
|---|---|---|
| уникальных product_id | 5000 (= весь каталог) | 5000 |
| unwound-строк (`items[]` по всем orders) | 600461 | 600461 |
| revenue из группировки | 376779509.73 | 376779509.73 |
| revenue независимой проверки (`$sum(orders.total)`, без unwind) | 376779509.73 | 376779509.73 |
| wall-latency | 462 ms | 492 ms |

Ассерт (прошёл на обоих клиентах): сумма `revenue` из `$unwind`+`$group`
СОВПАДАЕТ (diff=0.0000, допуск 0.01) с независимо посчитанным
`$sum(orders.total)` по НЕ-развёрнутой коллекции — один и тот же total,
посчитанный двумя разными путями агрегации, байт-в-байт сошёлся на обоих
клиентах.

**4. Лимит памяти blocking-стадии (100 МиБ), `$unwind`+`$sort` по
некоррелированным с индексом полям (~600К строк):**

| | Go | Java |
|---|---|---|
| С явным `allowDiskUse:false` | ошибка, код **292** (`QueryExceededMemoryLimitNoDiskUseAllowed`), 167 ms | ошибка, код **292**, 184 ms |
| С `allowDiskUse:true` | успех, 600461 строк, 3.31 s | успех, 600461 строк, 4.16 s |

Ассерты (прошли на обоих клиентах, оба направления): БЕЗ `allowDiskUse` —
реальная ошибка сервера с кодом 292; С `allowDiskUse:true` — реальный
успех, число строк совпадает с независимой проверкой `$unwind`+`$count`.

**Важная оговорка про серверный параметр `allowDiskUseByDefault` (реально
проверено вживую отдельным сравнением сырых команд через
`*event.CommandMonitor` при подготовке Go-стенда, НЕ предполагалось
заранее):** «без `allowDiskUse`» — это ИМЕННО поле `allowDiskUse` ЯВНО
равное `false`, а НЕ отсутствие поля в команде. Причина — легко упускаемый
серверный параметр **`allowDiskUseByDefault`, который по умолчанию `true`
начиная с MongoDB 6.0**: если per-query опция `allowDiskUse` вообще не
передана в команде, сервер подставляет значение этого параметра, а не
какое-то отдельное умолчание команды `aggregate`. На этом же датасете и той
же самой команде вариант с ПРОПУЩЕННЫМ полем `allowDiskUse` (клиент просто
не передаёт опцию) сервер обрабатывает УСПЕШНО за ~3.5 s — ровно потому,
что `allowDiskUseByDefault=true` разрешает спилл на диск без явного флага.
Только ЯВНО переданное на уровне команды `allowDiskUse:false` форсирует
старый жёсткий лимит 100 МиБ (код 292). Сравнение сырых команд (Go,
`*event.CommandMonitor`):

```
{"aggregate":"orders", ..., "allowDiskUse": false, ...}      -> код 292, ~150-220ms
{"aggregate":"orders", ...(поля allowDiskUse нет вовсе)...}  -> успех, ~3.5s, 600461 строк
{"aggregate":"orders", ..., "allowDiskUse": true, ...}       -> успех, ~3.4s, 600461 строк
```

Из-за этого оба стенда (Go и Java) ЯВНО передают `allowDiskUse:false` для
сценария «без флага» — простой пропуск опции (первая версия обоих стендов,
до этой проверки) давал ложный «успех» в обоих случаях (ожидаемо, раз
`allowDiskUseByDefault=true`) и валил ассерт «БЕЗ allowDiskUse должен
упасть», который в брифе имел в виду именно явный `false`.

**Ещё одна честная оговорка (тоже проверено вживую):** НЕ любой
blocking-аккумулятор можно вытолкнуть на диск через `allowDiskUse`. Тот же
объём (`$unwind` + `$group{_id:null, docs:{$push:"$$ROOT"}}` — один
аккумулятор на ВСЮ коллекцию, не много групп) падает С ОШИБКОЙ ДАЖЕ ПРИ
`allowDiskUse:true`: `"$push used too much memory and cannot spill to
disk. Memory limit: 104857600 bytes"`, код **146** (`ExceededMemoryLimit`,
НЕ 292). `allowDiskUse` спасает внешнюю сортировку (`$sort`) и
group-by-many-groups, но не единственный распухший `$push`-аккумулятор
одной группы — поэтому демонстрация лимита памяти в этой серии построена
на `$sort`, не на `$group`+`$push`.

Импорт: 50000/5000/200000 — совпадает с `dataset/manifest.json` (assert
прошёл, оба клиента). Датасет и версии образов — см. «Сквозные факты»
ниже; `org.mongodb:mongodb-driver-sync` 5.5.1 (Maven Central, сверено
живьём 2026-07-11).

## Стенд #5 — replica sets, oplog, concerns

Живой прогон `bash ops/replication-demo.sh` (Task 7) — rs0 (3 узла,
`mongo:8.2.11`) + `postgres:18`, полный датасет (`seed=42`: users=50000,
products=5000, orders=200000, см. `dataset/manifest.json`), `mongoimport`.
Бинарник `replication/main.go` работает в две фазы, разделяемые
demo-скриптом: **core** (oplog/write concern/причинная согласованность/
PG-контраст) на ещё здоровом кластере, затем **failover-write** — ПОСЛЕ
того, как demo-скрипт сам (`docker stop` контейнера primary + polling
`db.hello().isWritablePrimary` на выживших узлах) убедился, что новый
primary избран. FIXTURE-строки дословно (Go stdout стенда + FIXTURE-строка
самого demo-скрипта про перевыборы):

```
FIXTURE replication: import_users=50000 import_products=5000 import_orders=200000
FIXTURE replication: oplog_insert_latency=26.96459ms oplog_ns=cookbook.replication_demo oplog_op=i oplog_entry={"lsid":{"id":{"$binary":{"base64":"eEkTPavRSGuO/Ik8lIcmCw==","subType":"04"}},"uid":{"$binary":{"base64":"47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=","subType":"00"}}},"txnNumber":1,"op":"i","ns":"cookbook.replication_demo","ui":{"$binary":{"base64":"9C52/zeWT7iRJn54T6DoQA==","subType":"04"}},"o":{"_id":{"$oid":"6a52589a63c5afbaa8279058"},"marker":"oplog-demo","seq":1,"created_at":{"$date":"2026-07-11T14:52:10.799Z"}},"o2":{"_id":{"$oid":"6a52589a63c5afbaa8279058"}},"stmtId":0,"ts":{"$timestamp":{"t":1783781530,"i":3}},"t":1,"v":2,"wall":{"$date":"2026-07-11T14:52:10.82Z"},"prevOpTime":{"ts":{"$timestamp":{"t":0,"i":0}},"t":-1}}
FIXTURE replication: write_concern=w1 docs=200 avg_latency=549.681µs total_latency=109.936372ms
FIXTURE replication: write_concern=majority docs=200 avg_latency=4.100576ms total_latency=820.115324ms
FIXTURE replication: causal_with_session_found=true causal_with_latency=48.411451ms causal_with_aftercluster_time_in_command=true
FIXTURE replication: causal_without_session_found_immediately=true causal_without_latency=6.178495ms causal_without_aftercluster_time_in_command=false
FIXTURE replication: pg_wal_level=replica pg_max_wal_senders=10
FIXTURE replication: failover_stopped_primary=mongo1 failover_new_primary=mongo2 failover_election_time_approx=5s
FIXTURE replication: failover_write_primary=mongo2:27017 failover_write_success=true failover_write_latency=25.588872ms
```

### 1. Oplog — структура события `insert`

Один `insertOne` в `cookbook.replication_demo` → соответствующая запись,
прочитанная из `local.oplog.rs` НА PRIMARY (`readpref.Primary()`, oplog —
собственный журнал узла). Реальная форма записи (extended JSON, поля,
существенные для статьи):

| Поле | Значение (этот прогон) | Смысл |
|---|---|---|
| `op` | `"i"` | тип операции — insert |
| `ns` | `cookbook.replication_demo` | namespace (db.collection) |
| `o` | `{_id, marker:"oplog-demo", seq:1, created_at}` | сам вставленный документ |
| `o2` | `{_id}` | ключ, идентифицирующий документ (у insert — `_id`) |
| `ts` | `Timestamp(1783781530, 3)` | позиция в oplog (секунды + ординал), по ней реплики применяют операции по порядку |
| `t` | `1` | term выборов (election term), в котором запись сделана |
| `wall` | `2026-07-11T14:52:10.82Z` | wall-clock время применения |
| `v` | `2` | версия формата oplog-записи |
| `lsid`/`txnNumber`/`stmtId` | (retryable-write метаданные) | сессия + номер для идемпотентного повтора записи |

Ассерт (прошёл): `op=="i"`, `ns=="cookbook.replication_demo"`, поля
`ts`/`wall`/`o` присутствуют одновременно — запись породила oplog-entry
ожидаемого вида. Латентность самого `insertOne` — 27.0 ms (первая запись в
свежесозданную коллекцию, включает автосоздание коллекции).

### 2. Write concern — `w:1` vs `w:majority`

Серия из **200 ОДИНОЧНЫХ** `insertOne` каждым concern'ом (одиночных, не
bulk — ack ждётся на КАЖДУЮ запись, bulk смазал бы разницу):

| Write concern | avg-латентность на запись | total на 200 записей |
|---|---|---|
| `w:1` (ack только от primary) | **549.7 µs** | 109.9 ms |
| `w:majority` (ack от большинства узлов) | **4.10 ms** | 820.1 ms |

Ассерт (прошёл): `w:majority` **дороже** `w:1` — **7.46×** на этой
топологии (549.7 µs → 4.10 ms). На одном docker-хосте три узла всё же
обмениваются подтверждениями через сетевой стек контейнеров, и разница
оказалась уверенно наблюдаемой (ассерт «majority дороже» — мягкий, с
готовой оговоркой про шум single-host, но здесь она не понадобилась:
контраст чёткий). На реальной сети между AZ/датацентрами разрыв только
вырастет (majority ждёт самый медленный узел из кворума).

### 3. Read concern / причинная согласованность — **основной payoff стенда**

Причинно-согласованная сессия (`CausalConsistency=true`) делает
read-your-writes через **secondary** (`readpref.Secondary()`). Механизм
доказан не гонкой, а перехватом СЫРОЙ команды `find` через
`*event.CommandMonitor`:

| Вариант | документ найден на secondary | latency | `readConcern.afterClusterTime` в команде |
|---|---|---|---|
| **С** причинно-согласованной сессией | **да** (read-your-writes) | 48.4 ms | **есть** (проверено в сырой команде) |
| **БЕЗ** сессии (независимый read) | да (в этом прогоне) | 6.2 ms | **нет** (проверено) |

Ассерты (прошли):
- **С сессией** — документ РЕАЛЬНО виден при чтении с secondary сразу после
  записи, И перехваченная `find`-команда несла
  `readConcern.afterClusterTime` — это и есть механизм: сервер-secondary
  ДОЖИДАЕТСЯ применения нужной операции из oplog (до указанного
  cluster-time), прежде чем ответить. Гарантированное, детерминированное
  поведение драйвера, не «повезло с гонкой».
- **БЕЗ сессии** — `afterClusterTime` в команде НЕ появляется НИКОГДА
  (взяться неоткуда без сессии) — это жёсткий ассерт, а не наблюдение.

Латентность «с сессией» ВЫШЕ (48.4 ms против 6.2 ms без) — ровно потому,
что secondary при causal-чтении реально ждёт догона реплики до нужного
cluster-time, вместо того чтобы ответить из текущего (возможно, отстающего)
состояния.

**Честная оговорка (single-host, зафиксирована как наблюдение, НЕ ассерт —
бриф «may lag»):** БЕЗ причинной согласованности документ на secondary в
этом прогоне ВСЁ РАВНО нашёлся сразу (6.2 ms) — на одном docker-хосте
локальная репликация обычно опережает независимый клиентский read, поэтому
гонку «stale read с secondary» не каждый прогон ловит вживую. Риск от этого
не исчезает (на реальной сети/под нагрузкой secondary отстаёт заметно) —
именно поэтому доказательство построено на ДЕТЕРМИНИРОВАННОМ признаке
(наличие/отсутствие `afterClusterTime` в команде), а не на флаки-наблюдении
«нашёлся/не нашёлся».

### 4. Failover — реальные перевыборы (kill primary)

Demo-скрипт определил текущий primary (`mongo1`), сделал `docker stop`
контейнера `mongo1` и опрашивал `db.hello().isWritablePrimary` на двух
выживших узлах (`mongo2`/`mongo3` — 2 из 3 всё ещё кворум) до появления
нового primary:

| Метрика | Значение |
|---|---|
| остановленный primary | `mongo1` |
| новый primary (избран) | **`mongo2`** |
| приблизительное время перевыборов | **~5 s** (секундная точность bash-polling, включает пропущенные heartbeat'ы + election) |
| запись ПОСЛЕ re-election | **успешна**, обслужена `mongo2:27017`, latency 25.6 ms |

Ассерты (прошли): кластер восстановил primary (`mongo2` избран после
остановки `mongo1`), и запись фазы `failover-write` РЕАЛЬНО прошла на новый
primary (`InsertedID` вернулся, ошибки нет). Это сквозная демонстрация
живучести replica set: потеря узла-primary → автоматические перевыборы →
кластер снова принимает записи, без ручного вмешательства.

**Оговорка про время перевыборов:** ~5 s — это НЕ чистое election-time
сервера, а наблюдаемое снаружи полное окно «остановил контейнер → сосед
объявил себя primary», измеренное bash-polling'ом с шагом 1 s (включает
таймаут на пропущенные heartbeat'ы `electionTimeoutMillis`, по умолчанию
10 s, но фактический детект отказа ускоряется явным разрывом TCP при
`docker stop`, плюс сам election). Честное «порядка нескольких секунд», не
точная константа — фиксируется каждый прогон.

### 5. PG-контраст (опционально) — oplog vs логическая репликация

Один живой факт из `compose/postgres.yml`:

| Параметр | Значение образа `postgres:18` «из коробки» |
|---|---|
| `wal_level` | **`replica`** (НЕ `logical`) |
| `max_wal_senders` | `10` |

**Граница (реальный факт этой конфигурации, не общее правило):** `postgres:18`
поднимается с `wal_level=replica` по умолчанию — логическая репликация PG
(publication/subscription, декодирование WAL в построчные изменения)
требует ЯВНОГО `wal_level=logical` + перезапуск сервера, НЕ включена из
коробки. Контраст с MongoDB: **oplog пишется БЕЗУСЛОВНО** на любом узле
replica set сразу после `rs.initiate()`, без отдельного флага —
«встроено всегда» против «требует явного включения». Полноценный стенд
publication/subscription — за рамками этой задачи (граница на возможную
отдельную статью про PG-репликацию).

### Граница на серию «Транзакции»

Внутренности multi-document транзакций (snapshot isolation + write concern
транзакции) этот стенд НЕ строит — тот же понятийный аппарат
(`operationTime`/`afterClusterTime`, readConcern), что и у причинной
согласованности выше, но полноценный txn-стенд — в отдельной серии
«Транзакции и изоляция» (`databases/transactions`).

Импорт: 50000/5000/200000 — совпадает с `dataset/manifest.json` (assert
прошёл). Датасет и версии образов — см. «Сквозные факты» ниже.

## Стенд #6 — sharding вживую

Живой прогон `bash ops/sharding-demo.sh` (Task 8) — РЕАЛЬНЫЙ шардированный
кластер `mongo:8.2.11`: роутер `mongos1` + config-server replica set `csrs`
(1 узел, `--configsvr`) + два шарда-RS `shard1`/`shard2` (по 1 узлу,
`--shardsvr`). Все три RS инициируются вживую, оба шарда регистрируются,
затем шардируются ДВЕ коллекции ОДНОГО датасета `orders` (`seed=42`,
200000 заказов, см. `dataset/manifest.json`). Стенд `sharding/main.go`
подключается к РОУТЕРУ `mongos` (не к отдельному шарду) и меряет реальные
факты: распределение чанков, резолюцию запроса, движение балансировщика,
resharding. FIXTURE-строки дословно (Go stdout стенда):

```
FIXTURE sharding: shards=[shard1 shard2] shard_count=2
FIXTURE sharding: import_orders_hashed=200000 import_orders_ranged=200000 expected=200000
FIXTURE sharding: balancer_enabled_at_start=false
FIXTURE sharding: chunks_pre_hashed total=2 shards_covered=2 max_share=0.500 perShard{ shard1=1 shard2=1 }
FIXTURE sharding: chunks_pre_ranged total=1 shards_covered=1 max_share=1.000 perShard{ shard1=1 }
FIXTURE sharding: targeting_with_shard_key stage=SINGLE_SHARD shards_targeted=1
FIXTURE sharding: targeting_without_shard_key stage=SHARD_MERGE shards_targeted=2
FIXTURE sharding: chunks_post_hashed total=2 shards_covered=2 max_share=0.500 perShard{ shard1=1 shard2=1 }
FIXTURE sharding: chunks_post_ranged total=36 shards_covered=2 max_share=0.972 perShard{ shard1=1 shard2=35 }
FIXTURE sharding: balance_settle_time_approx=1m20s
FIXTURE sharding: ranged_movement pre{covered=1 max_share=1.000} post{covered=2 max_share=0.972}
FIXTURE sharding: hashed_movement pre{covered=2 max_share=0.500} post{covered=2 max_share=0.500}
FIXTURE sharding: reshard_target_ns=cookbook.orders_ranged key_before={"_id":1} new_key={user_id:hashed}
FIXTURE sharding: reshard_result=OK elapsed=5m29s key_after={"user_id":"hashed"} key_changed=true
```

### Как поднимается кластер (последовательность инициации)

`sharded.yml` в Task 1 задавал только валидную СТРУКТУРУ (для
`docker compose config`); фактическая инициация — здесь, в demo-скрипте:

1. `up -d sharded.yml` → 4 контейнера (`csrs1`/`shard1a`/`shard2a`/`mongos1`).
2. `rs.initiate()` КАЖДОГО RS в своей роли:
   `{_id:"csrs", configsvr:true, members:[{host:"csrs1:27017"}]}`,
   `{_id:"shard1", members:[{host:"shard1a:27017"}]}`,
   `{_id:"shard2", members:[{host:"shard2a:27017"}]}` — с ожиданием PRIMARY
   у каждого (`db.hello().isWritablePrimary`).
3. Роутер `mongos1` (стартует с `--configdb csrs/csrs1:27017`) дожидается
   готовности → `sh.addShard("shard1/shard1a:27017")` +
   `sh.addShard("shard2/shard2a:27017")` (`compose/init/shard-init.js`).
4. `sh.enableSharding("cookbook")`; размер чанка уменьшен до **1 МБ**
   (`config.settings {_id:"chunksize"}`), чтобы получить осмысленное число
   чанков; `sh.shardCollection` двух коллекций; `sh.stopBalancer()` ПЕРЕД
   импортом — чтобы pre-balance срез отражал чистую раскладку роутера.
5. `mongoimport` того же `orders.jsonl` в ОБЕ коллекции через `mongos`.

**Честная оговорка топологии:** каждый шард — 1-узловой RS (в проде это был
бы 3-узловой RS на шард). Config-server тоже 1 узел. Для демонстрации
шардирования (роутинг, чанки, балансировка, resharding) этого достаточно;
отказоустойчивость самих шардов — тема стенда #5 (replica sets).

### 1. Shard key hashed vs ranged — распределение чанков (главный контраст)

Обе коллекции — ОДИН датасет (200000 заказов), отличается только shard key.
`_id` заказов — **монотонный** ObjectID (последовательный счётчик, первый
`…200000000001`), то есть худший случай для ranged-ключа. Срез снят при
**выключенном** балансировщике — распределение ровно такое, каким его сделал
роутер по shard key НА ВСТАВКЕ (`config.chunks`, сгруппировано по `shard`):

| Коллекция | shard key | чанков всего | shard1 | shard2 | max-доля одного шарда |
|---|---|---|---|---|---|
| `orders_hashed` | `{_id: "hashed"}` | 2 | 1 | 1 | **0.500** (равномерно) |
| `orders_ranged` | `{_id: 1}` (ranged) | 1 | 1 | 0 | **1.000** (весь на одном шарде) |

Ассерт (прошёл): hashed охватывает **ОБА** шарда; ranged **концентрированнее**
hashed (`ranged.maxShare=1.000 > hashed.maxShare=0.500`). Механика: hashed-ключ
на `shardCollection` пустой коллекции сразу пресплитит начальные чанки по
шардам (по 1 на шард), и роутер раскладывает документы по хэшу `_id`
равномерно. Ranged-ключ стартует с ОДНОГО чанка `[minKey, maxKey)` на
primary-шарде; из-за монотонного `_id` ВСЕ вставки попадают в верхний чанк —
классический **write-хотспот**: 100% записи в один шард.

### 2. Резолюция запроса (targeting) — `explain` на mongos

`mongos` explain (`verbosity:"queryPlanner"`), верхняя стадия `winningPlan` и
число целевых шардов (`winningPlan.shards`):

| Запрос к `orders_hashed` | winningPlan.stage | целевых шардов |
|---|---|---|
| по shard key (`_id == <конкретный>`) | **`SINGLE_SHARD`** | **1** |
| без shard key (`status == "paid"`) | **`SHARD_MERGE`** | **2** |

Ассерт (прошёл): запрос по shard key роутится в **ровно 1 шард** (роутер
знает, где лежит документ по хэшу ключа); запрос без shard key — **scatter-
gather** по обоим шардам с последующим merge. Это ключевой практический
довод статьи: shard key нужно выбирать под самые частые запросы, иначе
каждый запрос веером идёт по всем шардам.

### 3. Балансировщик — движение чанков

Стенд включает балансировщик (`balancerStart`) и опрашивает
`balancerStatus` + `config.chunks`, пока раунд не сойдётся (стабильность
3 замера подряд при `inBalancerRound=false`). Сошлось за **~1m20s**:

| Коллекция | pre (shard1/shard2) | post (shard1/shard2) | покрытие шардов | max-доля |
|---|---|---|---|---|
| `orders_hashed` | 1 / 1 | 1 / 1 | 2→2 (без изменений) | 0.500→0.500 |
| `orders_ranged` | 1 / 0 | 1 / 35 | **1→2** | 1.000→0.972 |

hashed уже равномерен — балансировщику делать нечего. ranged: единственный
чанк на `shard1` разрезан на **36** и большая часть мигрирована на `shard2`
(покрытие 1→2 шарда). Наблюдение (реальное, не форсированный зелёный):
балансировщик распределил ranged по обоим шардам.

**Важная честная оговорка про числа (для статьи):** post-срез ranged по
**числу чанков** выглядит перекошенным на `shard2` (1 vs 35). Это НЕ значит,
что данные не сбалансированы: начиная с MongoDB 6.0 балансировщик оперирует
**объёмом данных**, а не числом чанков, — число чанков перестало быть
метрикой баланса. Балансировщик разрезал и перенёс объём; оставшийся на
`shard1` единственный чанк покрывает нижний диапазон `_id`. И это не отменяет
главного урока ranged-ключа: **write-хотспот** — про то, куда попадают НОВЫЕ
вставки (всегда верхний `maxKey`-чанк = один шард), а не про итоговое
распределение уже лежащих данных.

### 4. Resharding (8.x) — смена shard key вживую

`reshardCollection("cookbook.orders_ranged", {user_id: "hashed"})` — смена
shard key НА ЖИВОЙ шардированной коллекции без остановки:

| Параметр | Значение (этот прогон) |
|---|---|
| key_before | `{_id: 1}` |
| new_key | `{user_id: "hashed"}` |
| результат | **OK** (`config.collections.key` подтверждает смену) |
| длительность | **~5m29s** |

Ассерт-проверка (прошла): после успешного ответа `config.collections` для
`cookbook.orders_ranged` реально содержит новый ключ `{user_id:"hashed"}`.
Resharding — **тяжёлая** операция: координатор клонирует коллекцию под новый
ключ (фаза cloning доминирует), затем applying → blocking-writes →
committing. На этом минимальном стенде операция сидит прямо НА ГРАНИЦЕ
пятиминутного окна — в одной из предварительных живых попыток при клиентском
потолке 5 минут она НЕ успела и вернула `MaxTimeMSExpired` (~4m37s), поэтому
стенд использует потолок **8 минут** (клиентский таймаут не отменяет
операцию на сервере — resharding resumable). Итог: смена shard key вживую
**воспроизведена** (5m29s), но честно отмечаем её пограничную стоимость на
такой топологии.

**Дополнительная честная оговорка (устойчивость, не единичный сбой):**
переменность исхода подтверждена НЕ один раз. Финальный официальный прогон
для этой задачи (`ops/sharding-demo.sh`, полностью зафиксированный лог,
`reshard_target_ns=cookbook.orders_ranged key_before={"_id":1}
new_key={user_id:hashed}`) на ОДНОМ И ТОМ ЖЕ датасете и топологии НЕ уложился
в восьмиминутное окно: соединение с `mongos` оборвалось на **4m10s**
(`connection(mongos1:27017[-3]) incomplete read of message header:
connection closed unexpectedly by the other side: EOF`), стенд честно
зафиксировал `reshard_result=NOT_REPRODUCED elapsed=4m10s`, все ОСТАЛЬНЫЕ
сценарии (распределение чанков, targeting, балансировщик) в этом же прогоне
прошли идентично приведённым выше числам. Три живые попытки одного и того же
`reshardCollection` на этой топологии дали три разных по времени/исхода
результата (успех за 5m29s; `MaxTimeMSExpired` на ~4m37s; обрыв соединения на
4m10s) — совокупный вывод для статьи: `reshardCollection` на 200k документов
РАБОТАЕТ и МЕНЯЕТ shard key вживую (см. успешный прогон выше), но на
минимальной топологии (1-узловые shard RS, один хост) её стоимость настолько
близка к границе используемого клиентского окна ожидания, что исход
нестабилен от прогона к прогону — честный практический вывод: в продакшене
resharding крупных коллекций нужно either планировать с большим запасом по
времени, либо отслеживать прогресс асинхронно (`$currentOp`/
`reshardingFields.state` в `config.collections`) вместо блокирующего ожидания
ответа команды, поскольку операция resumable и продолжается на сервере
независимо от того, дождался ли её клиент.

## Стенд #7 — эксплуатация, драйверы, карта выбора

Живой прогон `bash ops/ops-demo.sh` (Task 9) — РЕАЛЬНЫЙ 3-узловой replica set
`mongo:8.2.11` (`rs0`), поверх уже импортированного датасета (`seed=42`,
users=50000, products=5000, orders=200000, см. `dataset/manifest.json`).
Стенд `ops-stand/main.go` (физически `mongodb/ops-stand/`, не `ops/` — там
только shell-скрипты серии, тот же приём, что и в `../clickhouse/ops-stand`)
+ Java-зеркало change streams `java/ops/`. FIXTURE-строки дословно (Go
stdout фазы `core`, Java stdout, demo-скрипт для backup):

```
FIXTURE ops: import_users=50000 import_products=5000 import_orders=200000
FIXTURE ops: pool_max_pool_size=3 pool_workers=9 pool_block_ms=200 pool_total_duration=623.892859ms pool_peak_checked_out=3
FIXTURE ops: cs_insert_op_type=insert cs_insert_latency=25.263424ms cs_insert_resume_token_present=true
FIXTURE ops: cs_resume_first_event_op_type=update cs_resume_latency=1.385043ms
FIXTURE ops: cs_delete_op_type=delete cs_delete_latency=6.030088ms
FIXTURE ops: retryable_writes_total=20 retryable_writes_success=20 retryable_writes_duration=8.746036956s retryable_writes_step_down_primary=mongo1:27017 retryable_writes_failed_attempts=1 retryable_writes_succeeded_attempts=20 retryable_writes_retried_txn_count=1 retryable_writes_max_attempts_per_txn=2
FIXTURE ops-java: cs_insert_op_type=OperationType{value='insert'} cs_insert_latency=PT0.107076543S cs_insert_resume_token_present=true
FIXTURE ops-java: cs_resume_first_event_op_type=OperationType{value='update'} cs_resume_latency=PT0.003465067S
FIXTURE ops-java: cs_delete_op_type=OperationType{value='delete'} cs_delete_latency=PT0.015272445S
FIXTURE ops: backup_dump_duration=1s
FIXTURE ops: backup_restore_duration=5s
FIXTURE ops: backup_orig_users=50000 backup_orig_products=5000 backup_orig_orders=200000 backup_restored_users=50000 backup_restored_products=5000 backup_restored_orders=200000 backup_restored_db=cookbook_restored
```

### 1. Пул соединений (maxPoolSize) — РЕАЛЬНАЯ задержка, не failpoint

**Честная методологическая оговорка:** сборка `mongo:8.2.11`, которую
использует весь cookbook, НЕ поддерживает `configureFailPoint`/
`failCommand` (`enableTestCommands` выключен на этом образе — проверено
живым вызовом `db.adminCommand({configureFailPoint:...})` ДО реализации
стенда: `MongoServerError: no such command: 'configureFailPoint'`). Вместо
синтетического failpoint стенд использует РЕАЛЬНУЮ серверную задержку:
`$where` с `sleep(200)` на коллекции ровно из ОДНОГО документа (JS-предикат
выполняется ровно один раз за запрос — детерминированные 200мс блокировки
на запрос).

9 конкурентных `FindOne` через клиента с `maxPoolSize=3`:

| Метрика | Значение |
|---|---|
| maxPoolSize | 3 |
| воркеров | 9 |
| задержка на запрос (сервер) | 200мс |
| суммарное время пачки | **623.9мс** |
| пик ОДНОВРЕМЕННО занятых соединений (`*event.PoolMonitor`) | **3** |

Ассерт (прошёл, оба жёсткие): пиковое число занятых соединений **никогда**
не превысило `maxPoolSize=3` (инвариант пула, доказан НАПРЯМУЮ через
`ConnectionCheckedOut`/`ConnectionCheckedIn`-события монитора, не по
догадке из тайминга); суммарное время пачки (623.9мс) значимо больше одного
раунда блокировки (200мс) — минимум 3 последовательных раунда очереди
(⌈9/3⌉=3), что при полном параллелизме заняло бы ~200мс, а заняло втрое
больше — прямое наблюдаемое доказательство, что пул РЕАЛЬНО ограничивает
конкурентность, а не просто держит счётчик.

### 2. Change streams (CDC) — insert/update/delete + доказательство resumeAfter

Watch на выделенной коллекции; ГЛАВНОЕ доказательство — не просто "получили
3 события", а **resumeAfter переживает простой консьюмера**: после
insert-события консьюмер закрывается НАМЕРЕННО, update происходит ПОКА
никто не слушает (имитация простоя), переоткрытие с `resumeAfter(token)`
обязано вернуть ИМЕННО это пропущенное update-событие первым.

| Событие | operationType | латентность (генерация → доставка) |
|---|---|---|
| insert | `insert` | Go: 25.3мс / Java: 107.1мс |
| update (после resumeAfter, простой консьюмера) | **`update`** (не `insert` повторно, не потеряно) | Go: 1.4мс / Java: 3.5мс |
| delete | `delete` | Go: 6.0мс / Java: 15.3мс |

Ассерт (прошёл, Go И Java независимо, каждый на своей коллекции — `cs_demo_go`
/ `cs_demo_java` — против ОДНОГО и того же живого кластера): все три
`operationType` пришли в правильном порядке; событие, ПЕРВЫМ пришедшее после
`resumeAfter(token сразу после insert)` — именно `update`, произошедший во
время простоя, а не дубль insert и не потеря. Java-числа заметно выше Go
(107мс vs 25мс на первый insert) — единственный раз в серии, ожидаемо связан
с JVM warm-up первого запроса (класс-загрузка/JIT), а не с драйвером как
таковым: разница исчезает уже на втором событии (resume: 3.5мс Java vs
1.4мс Go — тот же порядок величины).

### 3. Retryable writes переживают step-down primary — САМОЕ СИЛЬНОЕ доказательство стенда

Реальный `replSetStepDown` (force:true, secondaryCatchUpPeriodSecs:0)
текущего primary (`mongo1:27017`) КОНКУРЕНТНО с серией из 20 `InsertOne`
(retryWrites=true по умолчанию в URI mongo-go-driver/v2). Доказательство —
НЕ просто "итог успешный" (мог быть везением тайминга: соединение
переустановилось раньше первой попытки), а перехват через
`*event.CommandMonitor`: один и тот же `txnNumber` (одна и та же логическая
запись) встретился в **двух** `Started`-попытках insert — то есть попытка
РЕАЛЬНО провалилась на старом primary и была ПОВТОРЕНА драйвером на новом.

| Метрика | Значение |
|---|---|
| записей всего | 20 |
| успешных (итог) | **20/20** |
| суммарное время | 8.746s |
| остановленный primary | `mongo1:27017` |
| Started-попыток insert всего | 21 (одна запись потребовала 2 попытки) |
| Succeeded insert | 20 |
| записей с доказанным повтором (тот же txnNumber в >1 Started) | **1** |
| макс. попыток на одну запись | 2 |
| кластер восстановил primary после step-down | да (снова `mongo1:27017`, переизбран сам собой) |

Ассерт (прошёл, оба жёсткие): **все 20 из 20** записей в итоге вернули
успех клиенту несмотря на форсированный step-down primary "посреди" серии;
**минимум одна** запись доказанно (не предположительно) была прервана и
ПОВТОРЕНА драйвером — тот же `txnNumber` дважды в `CommandStartedEvent`.
Честная оговорка: остальные 19 записей либо успели пройти ДО step-down,
либо застали кластер уже без primary и дождались нового через обычный
server selection (до `serverSelectionTimeoutMS`, по умолчанию 30s) без
формального повтора команды — это ТОЖЕ retryable-writes-поведение
(driver-level retry — не единственный механизм устойчивости к недолгой
недоступности primary), но строгое доказательство "повтор одной и той же
операции" via `CommandMonitor` получено ровно для одной записи из 20 —
этого достаточно, чтобы утверждать механизм воспроизведён, а не
гипотетический.

### 4. Backup — mongodump/mongorestore round-trip

`mongodump --db=cookbook` (полный дамп, включая служебные demo-коллекции
стенда) → `mongorestore --nsFrom="cookbook.*" --nsTo="cookbook_restored.*"`
на том же `mongo1` (URI на весь `rs0`, автоматический выбор primary):

| Метрика | Значение |
|---|---|
| mongodump | **1s** (255021 документов, ~120 МБ) |
| mongorestore | **5s** |
| users (orig / restored) | 50000 / 50000 |
| products (orig / restored) | 5000 / 5000 |
| orders (orig / restored) | 200000 / 200000 |

Ассерт (прошёл, двойной): исходная база (`cookbook`) совпадает с
`dataset/manifest.json` ДО backup (сверка эталона); восстановленная база
(`cookbook_restored`) совпадает С ИСХОДНОЙ по всем трём коллекциям — round-
trip не потерял и не задублировал ни одного документа.

### Честная оговорка про образ mongo:8.2.11 (для статьи)

`configureFailPoint`/`failCommand` — стандартный инструмент тестирования
драйверов MongoDB (используется в официальных test suite всех языковых
драйверов), но требует сборки сервера с `enableTestCommands=1`, которой НЕТ
в публичном образе `mongo:8.2.11` с Docker Hub. Это значимо для статьи: кто
планирует переиспользовать failpoint-техники из документации/тестов
драйверов на СВОЁМ кластере — либо строит собственный образ с этим флагом,
либо (как этот стенд) добивается того же наблюдаемого эффекта (искусственная
серверная задержка) легальными средствами — здесь `$where`+`sleep()`.

---

## Сквозные факты (для всех статей серии)

### Версии

- Образ MongoDB: **`mongo:8.2.11`** (пин явный, `compose/replica-set.yml`,
  `compose/sharded.yml`).
- Образ PostgreSQL: **`postgres:18`** (`compose/postgres.yml`).
- Go-модули серии: `go 1.24` (`go.mod` каждого стенда), драйвер
  **`go.mongodb.org/mongo-driver/v2 v2.3.0`** (единая версия во всех
  модулях), сборочный образ гейта — `golang:1.25`.
- Java-реактор (`java/`, модули `aggregation` и `ops`): официальный
  синхронный драйвер **`org.mongodb:mongodb-driver-sync 5.5.1`** (Maven
  Central, сверено живьём 2026-07-11), `maven.compiler.release=25`,
  сборочный образ гейта — `maven:3.9-eclipse-temurin-25`.
- Датасет: единый генератор `dataset/main.go`, `seed=42` (users=50000,
  products=5000, orders=200000, см. `dataset/manifest.json`) — импортируется
  заново в каждый стенд его собственным `ops/*-demo.sh`.
- Хост: Docker Desktop/Windows, все контейнеры одного стенда — на одном
  физическом хосте (важно для честных оговорок ниже про сетевую задержку).

### Честные оговорки, общие для нескольких стендов

- **Однохостовая топология стирает часть сетевых различий.** Все узлы
  replica set/sharded-кластера — контейнеры одного Docker-хоста, без
  реальной межхостовой/меж-AZ задержки. Это видно в нескольких местах: PG
  оказался быстрее Mongo на единичном key-read (§1, 175.6 µs против
  264.7 µs — «не всё так просто», не общий вывод «Postgres быстрее»);
  `w:majority` дороже `w:1` уверенно даже на одном хосте (§5, ×7.46), но на
  реальной сети разрыв будет больше; secondary без причинной
  согласованности в этом прогоне УСПЕЛ вернуть свежий документ (§5, 6.2 ms)
  — доказательство read-your-writes поэтому построено на детерминированном
  признаке (`afterClusterTime` в сырой команде), а не на факте гонки.
  Направление эффектов везде архитектурно верное и воспроизведено, абсолютные
  величины — артефакт локальной топологии стенда.
- **WiredTiger cache/eviction — ресурс КОНКРЕТНОГО узла, не кластера, и
  пороги эвикции не всегда задеваются небольшой нагрузкой.** В §2 write-
  нагрузка ~100 МБ payload подняла dirty bytes и журнал, но НЕ спровоцировала
  активную эвикцию (пороги cache_size не пройдены за один прогон стенда) —
  честно зафиксировано как факт, не как баг ассерта.
- **`mongo:8.2.11` (публичный образ Docker Hub) не собран с
  `enableTestCommands=1`** → `configureFailPoint`/`failCommand` недоступны
  (`MongoServerError: no such command`). Везде, где брифу нужна
  контролируемая задержка/сбой (§7, maxPoolSize-контентион), стенд использует
  легальную замену — реальную серверную задержку (`$where`+`sleep()`) или
  реальное действие (`replSetStepDown`, `docker stop` контейнера primary в
  §5/§7), а не синтетический failpoint.
- **Sharding/resharding — единственный источник нестабильных по времени
  чисел серии (§6).** Распределение чанков, targeting и работа балансировщика
  воспроизводятся идентично от прогона к прогону; `reshardCollection` на
  200k документов на минимальной 1-узловой на-шард топологии — нет: три живые
  попытки дали три разных исхода (успех 5m29s, `MaxTimeMSExpired` ~4m37s,
  обрыв соединения на 4m10s). Итоговая FIXTURE-строка — последний официальный
  прогон демо-скрипта (`NOT_REPRODUCED elapsed=4m10s`), успешный прогон
  задокументирован отдельно как доказательство, что операция в принципе
  работает. Для статьи это не баг стенда, а честный практический вывод про
  стоимость resharding на границе клиентского окна ожидания.
- **JVM warm-up — единственная системная разница Go/Java чисел серии (§7).**
  Первый вызов Java-клиента в change streams заметно медленнее Go (107 мс
  против 25 мс на первый insert), но разница исчезает уже на втором событии
  (3.5 мс против 1.4 мс — тот же порядок) — класс-загрузка/JIT, не свойство
  драйвера. Во всех остальных местах, где есть Go/Java-зеркало (§4
  aggregation, §7 change streams), количественные результаты (число
  документов, keysExamined, коды ошибок) совпадают байт-в-байт между
  клиентами — расходится только latency первого прогона.
- **`allowDiskUseByDefault=true` с MongoDB 6.0 — не обход документации, а
  дефолт сервера (§4).** Отсутствие поля `allowDiskUse` в команде `aggregate`
  — НЕ то же самое, что `allowDiskUse:false`; стенды §4 поэтому явно
  передают `false`, когда демонстрируют жёсткий лимит памяти 100 МиБ. Тот же
  лимит НЕ обходится `allowDiskUse:true` для одиночного распухшего
  `$push`-аккумулятора (код 146, отдельный от 292) — `allowDiskUse` спасает
  внешнюю сортировку и group-by-many-groups, но не любой blocking-
  аккумулятор.
