package tech.khorost.mongodb.aggregation;

import com.mongodb.ExplainVerbosity;
import com.mongodb.MongoCommandException;
import com.mongodb.client.AggregateIterable;
import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import com.mongodb.client.MongoCollection;
import com.mongodb.client.MongoCursor;
import com.mongodb.client.MongoDatabase;
import com.mongodb.client.model.Accumulators;
import com.mongodb.client.model.Aggregates;
import com.mongodb.client.model.Filters;
import com.mongodb.client.model.IndexOptions;
import com.mongodb.client.model.Indexes;
import com.mongodb.client.model.Sorts;
import org.bson.Document;
import org.bson.conversions.Bson;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Java-зеркало ../../aggregation (Go) — стенд #4 серии "MongoDB: глубокое
 * погружение": pipeline {@code $match->$group->$sort} (использование
 * индекса на {@code $match}), цена {@code $lookup} как join vs
 * эквивалентная агрегация по уже денормализованным полям, {@code $unwind}
 * + {@code $group} по items[], и лимит памяти blocking-стадий (100 МиБ) —
 * с/без {@code allowDiskUse} — на РЕАЛЬНОМ 3-узловом replica set (rs0)
 * поверх уже импортированного датасета. ТОТ ЖЕ пайплайн, что и Go-стенд —
 * подтверждает, что числа/поведение сервера не артефакт конкретного
 * драйвера, а свойство сервера.
 *
 * <p>Explain — через встроенный {@link AggregateIterable#explain(ExplainVerbosity)}
 * официального синхронного драйвера (в отличие от Go-стенда, у которого
 * mongo-go-driver/v2 не имеет высокоуровневого метода explain для
 * aggregate и приходится вручную собирать {@code explain} команду через
 * {@code RunCommand} — см. комментарий в ../../aggregation/main.go). Форма
 * ответа СЕРВЕРА идентична в обоих случаях (это одна и та же команда
 * {@code explain} на сервере) — верхний уровень содержит {@code "stages"},
 * статистические поля {@code $lookup}/{@code $sort}-записей лежат РЯДОМ с
 * ключом стадии (не вложены внутрь её конфигурации), а у {@code $cursor}
 * — {@code queryPlanner}/{@code executionStats} вложены ВНУТРЬ значения
 * {@code $cursor} (см. те же пометки в Go-стенде, проверено вживую один
 * раз и переиспользовано здесь).
 *
 * <p>Оговорка про лимит памяти (см. package-doc ../../aggregation/main.go
 * подробнее) — сервер спиллит на диск без явного флага благодаря
 * серверному параметру {@code allowDiskUseByDefault} (по умолчанию
 * {@code true} начиная с MongoDB 6.0), поэтому ЯВНО переданный
 * {@code allowDiskUse:false} (не пропуск опции) обязателен, чтобы реально
 * воспроизвести ошибку сервера; здесь —
 * {@code AggregateIterable#allowDiskUse(Boolean.FALSE)}.
 */
public final class Main {

    private static final String DB_NAME = "cookbook";
    private static final String MANIFEST_PATH = "../../dataset/manifest.json";

    private Main() {
    }

    private record Manifest(long seed, int users, int products, int orders) {
    }

    public static void main(String[] args) throws IOException {
        Manifest manifest = loadManifest();
        log("manifest: seed=%d users=%d products=%d orders=%d".formatted(
                manifest.seed(), manifest.users(), manifest.products(), manifest.orders()));

        String mongoUri = mustEnv("MONGO_URI");
        try (MongoClient client = MongoClients.create(mongoUri)) {
            MongoDatabase db = client.getDatabase(DB_NAME);
            MongoCollection<Document> usersColl = db.getCollection("users");
            MongoCollection<Document> productsColl = db.getCollection("products");
            MongoCollection<Document> ordersColl = db.getCollection("orders");

            assertImportCounts(manifest, usersColl, productsColl, ordersColl);
            pipelineScenario(db, ordersColl);
            lookupScenario(db, ordersColl);
            unwindGroupScenario(ordersColl);
            memoryLimitScenario(ordersColl);
        }
        log("готово.");
    }

    // -- инфраструктура -------------------------------------------------

    private static String mustEnv(String key) {
        String v = System.getenv(key);
        if (v == null || v.isEmpty()) {
            throw new IllegalStateException("обязательная переменная окружения " + key + " не задана");
        }
        return v;
    }

    private static void log(String msg) {
        System.out.println(Instant.now() + " " + msg);
    }

    // Стенд всегда запускается с cwd=java/aggregation/ (см.
    // ../../ops/aggregation-demo.sh: -w /app/java/aggregation в контейнере
    // maven:3.9-eclipse-temurin-25, где /app — весь каталог mongodb/),
    // поэтому ../../dataset/manifest.json всегда рядом — тот же приём, что
    // и ../dataset/manifest.json в Go-стендах (там cwd на один уровень
    // ближе к mongodb/).
    private static Manifest loadManifest() throws IOException {
        String raw = Files.readString(Path.of(MANIFEST_PATH));
        long seed = extractLong(raw, "seed");
        int users = (int) extractLong(raw, "users");
        int products = (int) extractLong(raw, "products");
        int orders = (int) extractLong(raw, "orders");
        return new Manifest(seed, users, products, orders);
    }

    // manifest.json — плоский JSON с 4 числовыми полями (см.
    // ../../dataset/main.go) — регулярка вместо JSON-библиотеки достаточна
    // и не тянет лишнюю зависимость ради 4 чисел.
    private static long extractLong(String json, String key) {
        Matcher m = Pattern.compile("\"" + key + "\"\\s*:\\s*(-?\\d+)").matcher(json);
        if (!m.find()) {
            throw new IllegalStateException("manifest.json: поле \"" + key + "\" не найдено");
        }
        return Long.parseLong(m.group(1));
    }

    private static void assertImportCounts(Manifest m, MongoCollection<Document> usersColl,
                                            MongoCollection<Document> productsColl, MongoCollection<Document> ordersColl) {
        long uc = usersColl.countDocuments();
        long pc = productsColl.countDocuments();
        long oc = ordersColl.countDocuments();
        System.out.printf("FIXTURE aggregation-java: import_users=%d import_products=%d import_orders=%d%n", uc, pc, oc);
        if (uc != m.users() || pc != m.products() || oc != m.orders()) {
            throw new IllegalStateException("assert: счётчики импорта должны совпасть с manifest.json (users=%d/%d products=%d/%d orders=%d/%d)"
                    .formatted(uc, m.users(), pc, m.products(), oc, m.orders()));
        }
        log("assert OK: импорт совпадает с manifest.json (users=%d products=%d orders=%d)".formatted(uc, pc, oc));
    }

    // -- explain-инфраструктура (парсинг Document-дерева ответа explain) --

    @SuppressWarnings("unchecked")
    private static Document asDoc(Object v) {
        return (v instanceof Document d) ? d : null;
    }

    @SuppressWarnings("unchecked")
    private static List<Object> asList(Object v) {
        return (v instanceof List<?> l) ? (List<Object>) l : null;
    }

    private static List<Document> pipelineStages(Document explainOut) {
        List<Object> raw = asList(explainOut.get("stages"));
        if (raw == null) {
            throw new IllegalStateException("explain aggregate: ответ не содержит \"stages\"");
        }
        List<Document> stages = new ArrayList<>(raw.size());
        for (Object el : raw) {
            Document d = asDoc(el);
            if (d != null) {
                stages.add(d);
            }
        }
        return stages;
    }

    // findPipelineStage — см. подробный комментарий в ../../aggregation/main.go
    // (findPipelineStage): возвращает ЦЕЛИКОМ запись стадии (не только
    // значение под wantKey) — статистические поля $lookup/$sort лежат
    // РЯДОМ с ключом стадии в той же записи, а не внутри его значения.
    private static Document findPipelineStage(List<Document> stages, String wantKey) {
        for (Document d : stages) {
            if (d.containsKey(wantKey)) {
                return d;
            }
        }
        return null;
    }

    private static List<Document> stagesAfter(List<Document> stages, String afterKey) {
        for (int i = 0; i < stages.size(); i++) {
            if (stages.get(i).containsKey(afterKey)) {
                return (i + 1 < stages.size()) ? stages.subList(i + 1, stages.size()) : List.of();
            }
        }
        return List.of();
    }

    private static List<String> stageKeys(List<Document> stages) {
        List<String> keys = new ArrayList<>(stages.size());
        for (Document d : stages) {
            for (String k : d.keySet()) {
                keys.add(k);
                break;
            }
        }
        return keys;
    }

    private static Document findStage(Object v, String name) {
        Document d = asDoc(v);
        if (d == null) {
            return null;
        }
        if (name.equals(d.getString("stage"))) {
            return d;
        }
        Object inputStage = d.get("inputStage");
        if (inputStage != null) {
            Document found = findStage(inputStage, name);
            if (found != null) {
                return found;
            }
        }
        List<Object> inputStages = asList(d.get("inputStages"));
        if (inputStages != null) {
            for (Object s : inputStages) {
                Document found = findStage(s, name);
                if (found != null) {
                    return found;
                }
            }
        }
        return null;
    }

    private static boolean hasStage(Object v, String name) {
        return findStage(v, name) != null;
    }

    // planTree — см. planTree в ../../aggregation/main.go: у aggregate
    // explain (executionStats, SBE-движок) winningPlan обёрнут
    // {isCached, queryPlan: {...дерево...}, slotBasedPlan: {...}}; если
    // "queryPlan" отсутствует (классический движок) — используем
    // winningPlan как есть.
    private static Document planTree(Document winningPlan) {
        Document qp = asDoc(winningPlan.get("queryPlan"));
        return qp != null ? qp : winningPlan;
    }

    private static long toLong(Object v) {
        if (v instanceof Integer i) {
            return i.longValue();
        }
        if (v instanceof Long l) {
            return l;
        }
        if (v instanceof Double d) {
            return d.longValue();
        }
        return 0L;
    }

    // -- Сценарий 1: pipeline $match -> $group -> $sort ------------------

    private static void pipelineScenario(MongoDatabase db, MongoCollection<Document> ordersColl) {
        final String idxName = "idx_status_agg";
        ordersColl.createIndex(Indexes.ascending("status"), new IndexOptions().name(idxName));
        log("создан/подтверждён индекс %s на orders.status (Java, тот же индекс, что Go-стенд)".formatted(idxName));

        List<Bson> pipeline = List.of(
                Aggregates.match(Filters.eq("status", "paid")),
                Aggregates.group("$user_id",
                        Accumulators.sum("totalSpent", "$total"),
                        Accumulators.sum("orderCount", 1)),
                Aggregates.sort(Sorts.descending("totalSpent"))
        );

        Document explainOut = ordersColl.aggregate(pipeline).explain(ExplainVerbosity.EXECUTION_STATS);
        List<Document> stages = pipelineStages(explainOut);

        Document cursorEntry = findPipelineStage(stages, "$cursor");
        if (cursorEntry == null) {
            throw new IllegalStateException("assert: explain aggregate должен содержать стадию \"$cursor\" (top-level ключи: " + stageKeys(stages) + ")");
        }
        Document cursorVal = asDoc(cursorEntry.get("$cursor"));
        if (cursorVal == null) {
            throw new IllegalStateException("assert: значение \"$cursor\" отсутствует или неожиданного типа");
        }
        Document qp = asDoc(cursorVal.get("queryPlanner"));
        if (qp == null) {
            throw new IllegalStateException("assert: \"$cursor\".queryPlanner отсутствует или неожиданного типа");
        }
        Document winningPlan = asDoc(qp.get("winningPlan"));
        if (winningPlan == null) {
            throw new IllegalStateException("assert: \"$cursor\".queryPlanner.winningPlan отсутствует или неожиданного типа");
        }
        Document tree = planTree(winningPlan);

        Document ixscan = findStage(tree, "IXSCAN");
        boolean foundIx = ixscan != null;
        boolean collscanPresent = hasStage(tree, "COLLSCAN");
        String indexName = foundIx ? ixscan.getString("indexName") : null;

        Document es = asDoc(cursorVal.get("executionStats"));
        long nReturned = 0, keysExamined = 0, docsExamined = 0;
        if (es != null) {
            nReturned = toLong(es.get("nReturned"));
            keysExamined = toLong(es.get("totalKeysExamined"));
            docsExamined = toLong(es.get("totalDocsExamined"));
        }

        boolean groupIsSeparateStage = findPipelineStage(stages, "$group") != null;
        Document sortStage = findPipelineStage(stages, "$sort");
        boolean sortUsedDisk = false;
        long sortSpills = 0;
        if (sortStage != null) {
            Object ud = sortStage.get("usedDisk");
            sortUsedDisk = Boolean.TRUE.equals(ud);
            sortSpills = toLong(sortStage.get("spills"));
        }

        Instant t0 = Instant.now();
        List<Document> rows = new ArrayList<>();
        try (MongoCursor<Document> cur = ordersColl.aggregate(pipeline).iterator()) {
            while (cur.hasNext()) {
                rows.add(cur.next());
            }
        }
        Duration wall = Duration.between(t0, Instant.now());

        long sumOrderCount = 0;
        for (Document r : rows) {
            sumOrderCount += toLong(r.get("orderCount"));
        }

        System.out.printf("FIXTURE aggregation-java: pipeline_match_status=paid index=%s ixscan_found=%s collscan_present=%s group_merged_into_cursor=%s n_returned_groups=%d keys_examined=%d docs_examined=%d sort_used_disk=%s sort_spills=%d wall_latency=%s result_groups=%d sum_order_count=%d%n",
                indexName, foundIx, collscanPresent, !groupIsSeparateStage, nReturned, keysExamined, docsExamined, sortUsedDisk, sortSpills, wall, rows.size(), sumOrderCount);

        if (!foundIx || !idxName.equals(indexName)) {
            throw new IllegalStateException("assert: $match(status=paid) должен использовать IXSCAN(%s), got found=%s index=%s".formatted(idxName, foundIx, indexName));
        }
        if (collscanPresent) {
            throw new IllegalStateException("assert: план $match->$group->$sort НЕ должен содержать COLLSCAN");
        }
        if (es != null && keysExamined != docsExamined) {
            throw new IllegalStateException("assert: равенство-фильтр status=paid по единственному индексному полю должен давать keysExamined==docsExamined, got keys=%d docs=%d".formatted(keysExamined, docsExamined));
        }
        if (es != null && docsExamined != sumOrderCount) {
            throw new IllegalStateException("assert: totalDocsExamined explain'а (%d) должен совпасть с суммой orderCount по фактическому результату (%d)".formatted(docsExamined, sumOrderCount));
        }
        log("assert OK: $match(status=paid) -> IXSCAN(%s), без COLLSCAN, keysExamined==docsExamined==%d==sum(orderCount); wall=%s, групп=%d"
                .formatted(indexName, docsExamined, wall, rows.size()));
    }

    // -- Сценарий 2: $lookup как join ------------------------------------

    private static void lookupScenario(MongoDatabase db, MongoCollection<Document> ordersColl) {
        List<Bson> withLookup = List.of(
                Aggregates.unwind("$items"),
                Aggregates.lookup("products", "items.product_id", "_id", "product_doc"),
                Aggregates.unwind("$product_doc"),
                Aggregates.group("$product_doc.category",
                        Accumulators.sum("revenue", new Document("$multiply", List.of("$items.qty", "$items.price"))),
                        Accumulators.sum("lines", 1))
        );
        List<Bson> withoutLookup = List.of(
                Aggregates.unwind("$items"),
                Aggregates.group("$items.product_name",
                        Accumulators.sum("revenue", new Document("$multiply", List.of("$items.qty", "$items.price"))),
                        Accumulators.sum("lines", 1))
        );

        Document explainOut = ordersColl.aggregate(withLookup).explain(ExplainVerbosity.EXECUTION_STATS);
        List<Document> stages = pipelineStages(explainOut);
        Document lookupEntry = findPipelineStage(stages, "$lookup");
        if (lookupEntry == null) {
            throw new IllegalStateException("assert: explain aggregate должен содержать стадию \"$lookup\" (top-level ключи: " + stageKeys(stages) + ")");
        }
        long lookupDocsExamined = toLong(lookupEntry.get("totalDocsExamined"));
        long lookupKeysExamined = toLong(lookupEntry.get("totalKeysExamined"));
        long lookupCollScans = toLong(lookupEntry.get("collectionScans"));
        long lookupNReturned = toLong(lookupEntry.get("nReturned"));
        long lookupStageMs = toLong(lookupEntry.get("executionTimeMillisEstimate"));
        Object indexesUsed = lookupEntry.get("indexesUsed");
        boolean unwindAfterLookupIsSeparate = findPipelineStage(stagesAfter(stages, "$lookup"), "$unwind") != null;

        Instant t0 = Instant.now();
        List<Document> lookupRows = new ArrayList<>();
        try (MongoCursor<Document> cur = ordersColl.aggregate(withLookup).iterator()) {
            while (cur.hasNext()) {
                lookupRows.add(cur.next());
            }
        }
        Duration lookupWall = Duration.between(t0, Instant.now());

        t0 = Instant.now();
        List<Document> noLookupRows = new ArrayList<>();
        try (MongoCursor<Document> cur = ordersColl.aggregate(withoutLookup).iterator()) {
            while (cur.hasNext()) {
                noLookupRows.add(cur.next());
            }
        }
        Duration noLookupWall = Duration.between(t0, Instant.now());

        double ratio = (double) lookupWall.toNanos() / (double) noLookupWall.toNanos();

        System.out.printf("FIXTURE aggregation-java: lookup_docs_examined=%d lookup_keys_examined=%d lookup_collection_scans=%d lookup_n_returned=%d lookup_indexes_used=%s lookup_stage_time_estimate=%dms lookup_unwind_merged_into_lookup=%s lookup_wall_latency=%s lookup_groups=%d nolookup_wall_latency=%s nolookup_groups=%d cost_ratio=%.1fx%n",
                lookupDocsExamined, lookupKeysExamined, lookupCollScans, lookupNReturned, indexesUsed, lookupStageMs, !unwindAfterLookupIsSeparate, lookupWall, lookupRows.size(), noLookupWall, noLookupRows.size(), ratio);

        if (lookupDocsExamined == 0) {
            throw new IllegalStateException("assert: $lookup должен реально сканировать документы products (totalDocsExamined=0)");
        }
        if (lookupCollScans != 0) {
            throw new IllegalStateException("assert: $lookup на products по _id (всегда индексировано) НЕ должен делать collectionScans, got " + lookupCollScans);
        }
        if (lookupWall.compareTo(noLookupWall) <= 0) {
            throw new IllegalStateException("assert: $lookup-путь должен быть ДОРОЖЕ эквивалентной агрегации без join, got lookup=%s <= no_lookup=%s".formatted(lookupWall, noLookupWall));
        }
        log("assert OK: $lookup реально сканирует products через индекс (%s, docsExamined=%d, collectionScans=0) и дороже эквивалента без join в %.1fx (%s vs %s)"
                .formatted(indexesUsed, lookupDocsExamined, ratio, lookupWall, noLookupWall));
    }

    // -- Сценарий 3: $unwind + $group по items[] -------------------------

    private static void unwindGroupScenario(MongoCollection<Document> ordersColl) {
        List<Bson> pipeline = List.of(
                Aggregates.unwind("$items"),
                Aggregates.group("$items.product_id",
                        Accumulators.sum("totalQty", "$items.qty"),
                        Accumulators.sum("totalRevenue", new Document("$multiply", List.of("$items.qty", "$items.price"))),
                        Accumulators.sum("lines", 1))
        );

        Instant t0 = Instant.now();
        List<Document> rows = new ArrayList<>();
        try (MongoCursor<Document> cur = ordersColl.aggregate(pipeline).iterator()) {
            while (cur.hasNext()) {
                rows.add(cur.next());
            }
        }
        Duration wall = Duration.between(t0, Instant.now());

        double sumRevenue = 0;
        long sumLines = 0;
        for (Document r : rows) {
            sumRevenue += toDouble(r.get("totalRevenue"));
            sumLines += toLong(r.get("lines"));
        }

        List<Bson> crossPipeline = List.of(
                Aggregates.group(null, Accumulators.sum("s", "$total"))
        );
        Document crossRow;
        try (MongoCursor<Document> cur = ordersColl.aggregate(crossPipeline).iterator()) {
            if (!cur.hasNext()) {
                throw new IllegalStateException("cross-check $sum(orders.total): ожидалась 1 строка, получено 0");
            }
            crossRow = cur.next();
            if (cur.hasNext()) {
                throw new IllegalStateException("cross-check $sum(orders.total): получено больше 1 строки");
            }
        }
        double crossTotal = toDouble(crossRow.get("s"));

        double diff = Math.abs(sumRevenue - crossTotal);

        System.out.printf("FIXTURE aggregation-java: unwind_group_distinct_products=%d unwind_group_lines=%d unwind_group_sum_revenue=%.2f cross_check_sum_orders_total=%.2f diff=%.4f wall_latency=%s%n",
                rows.size(), sumLines, sumRevenue, crossTotal, diff, wall);

        final double epsilon = 0.01;
        if (diff > epsilon) {
            throw new IllegalStateException("assert: сумма revenue из $unwind+$group (%.2f) должна совпасть с $sum(orders.total) (%.2f), разница=%.4f".formatted(sumRevenue, crossTotal, diff));
        }
        log("assert OK: $unwind+$group revenue (%.2f) совпадает с независимым $sum(orders.total) (%.2f), diff=%.4f <= %.2f; %d уникальных product_id, wall=%s"
                .formatted(sumRevenue, crossTotal, diff, epsilon, rows.size(), wall));
    }

    private static double toDouble(Object v) {
        if (v instanceof Number n) {
            return n.doubleValue();
        }
        return 0.0;
    }

    // -- Сценарий 4: лимит памяти blocking-стадии (100 МиБ) --------------

    private static void memoryLimitScenario(MongoCollection<Document> ordersColl) {
        List<Bson> countPipeline = List.of(Aggregates.unwind("$items"), Aggregates.count("n"));
        long expectedItems;
        try (MongoCursor<Document> cur = ordersColl.aggregate(countPipeline).iterator()) {
            if (!cur.hasNext()) {
                throw new IllegalStateException("$unwind+$count: ожидалась 1 строка, получено 0");
            }
            expectedItems = toLong(cur.next().get("n"));
        }
        log("ожидаемое число unwound-строк (items[] по всем orders): " + expectedItems);

        List<Bson> sortPipeline = List.of(
                Aggregates.unwind("$items"),
                Aggregates.sort(new Document("items.qty", 1).append("items.product_name", 1).append("created_at", -1))
        );

        // -- С ЯВНЫМ allowDiskUse:false: ожидаем ошибку сервера (лимит 100
        // МиБ). Как и в Go-стенде — ИМЕННО явное false, не пропуск опции
        // (см. package-doc и ../../aggregation/main.go: пропуск опции на
        // этом сервере ведёт себя как allowDiskUse:true из-за серверного
        // параметра allowDiskUseByDefault=true по умолчанию с MongoDB 6.0,
        // что подтверждено отдельным сравнением сырых команд при
        // подготовке Go-стенда).
        Instant t0 = Instant.now();
        String noDiskErr = null;
        int noDiskCode = 0;
        try (MongoCursor<Document> cur = ordersColl.aggregate(sortPipeline).allowDiskUse(Boolean.FALSE).iterator()) {
            while (cur.hasNext()) {
                cur.next();
            }
        } catch (MongoCommandException e) {
            noDiskErr = e.getMessage();
            noDiskCode = e.getCode();
        }
        Duration noDiskDur = Duration.between(t0, Instant.now());

        System.out.printf("FIXTURE aggregation-java: memory_limit_no_diskuse_error=%s memory_limit_no_diskuse_code=%d memory_limit_no_diskuse_latency=%s%n",
                quoteOrNone(noDiskErr), noDiskCode, noDiskDur);

        if (noDiskErr == null) {
            throw new IllegalStateException("assert: $unwind+$sort по ~%d строкам С allowDiskUse:false должен упереться в лимит памяти (100 МиБ) и вернуть ошибку, но прошёл успешно".formatted(expectedItems));
        }
        if (noDiskCode != 292) {
            throw new IllegalStateException("assert: ошибка С allowDiskUse:false должна иметь код 292 (QueryExceededMemoryLimitNoDiskUseAllowed), got code=%d err=%s".formatted(noDiskCode, noDiskErr));
        }
        log("assert OK: С allowDiskUse:false — реальная ошибка сервера (код %d): %s".formatted(noDiskCode, noDiskErr));

        // -- С allowDiskUse:true: ожидаем успех, тот же набор строк. --
        t0 = Instant.now();
        long actualItems = 0;
        try (MongoCursor<Document> cur = ordersColl.aggregate(sortPipeline).allowDiskUse(Boolean.TRUE).iterator()) {
            while (cur.hasNext()) {
                cur.next();
                actualItems++;
            }
        }
        Duration diskDur = Duration.between(t0, Instant.now());

        System.out.printf("FIXTURE aggregation-java: memory_limit_with_diskuse_success=true memory_limit_with_diskuse_rows=%d memory_limit_with_diskuse_latency=%s expected_rows=%d%n",
                actualItems, diskDur, expectedItems);

        if (actualItems != expectedItems) {
            throw new IllegalStateException("assert: С allowDiskUse:true число возвращённых строк (%d) должно совпасть с независимым $unwind+$count (%d)".formatted(actualItems, expectedItems));
        }
        log("assert OK: С allowDiskUse:true — внешняя сортировка прошла успешно за %s, вернула %d строк (совпадает с $unwind+$count)".formatted(diskDur, actualItems));
    }

    private static String quoteOrNone(String s) {
        return s == null ? "<none>" : "\"" + s.replace("\"", "'") + "\"";
    }
}
