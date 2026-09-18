package tech.khorost.inmemory;

// Стенд Apache Ignite: data grid + compute grid. Единственный Java-стенд
// серии "Вычисления в оперативной памяти" — остальные на Go. Ветка 2.x
// выбрана намеренно (Ignite 3.x полностью переписан, API несовместим,
// см. README). Общий источник истины — PostgreSQL (ORIGIN_DSN), общий
// датасет из dataset/.
//
// Три сценария (-scenario=cache|affinity|compute).
//
// ОТКЛОНЕНИЕ ОТ БРИФА (сверено javap по фактическому ignite-core-2.18.0.jar,
// не выдумано): бриф просил тонкий клиент (Ignition.startClient) для ВСЕХ
// трёх сценариев. Тонкий клиент (org.apache.ignite.client.IgniteClient) в
// 2.18.0 не имеет ни Affinity API (ignite.affinity(...).mapKeyToNode —
// метода affinity() на IgniteClient просто нет), ни IgniteCompute.affinityCall
// (у ClientCompute есть только execute(taskName, arg) — обычный, не
// affinity-aware вызов). Обе операции — часть "толстого" API
// (org.apache.ignite.Ignite), доступного либо серверной, либо клиентской
// ноде классического протокола (IgniteConfiguration.setClientMode(true) —
// узел присоединяется к discovery-кольцу, но не хранит данные). Поэтому:
//   - cache        — тонкий клиент (Ignition.startClient), как в брифе;
//   - affinity/compute — классический клиент-нода (Ignition.start с
//     clientMode=true), с тем же статическим TcpDiscoveryVmIpFinder, что и
//     у серверов.
//
// TopByCategoryTask и CategoryFilter (ScanQuery-предикат сценария compute)
// как классы деплоятся на серверные ноды ЗАРАНЕЕ — через classpath
// ($IGNITE_HOME/libs/, см. compose/ignite.yml), а не через peer class
// loading. Живой прогон показал, что P2P-деплой (peerClassLoadingEnabled=
// true с обеих сторон) поднимает клиент-ноду и даже пропускает
// IgniteCompute.affinityCall, но ScanQuery-предикат НЕ десериализуется на
// сервере (BinaryInvalidTypeException/ClassNotFoundException) — см.
// task-6-report.md. Classpath-деплой обходит эту границу целиком и вдобавок
// ближе к прод-эксплуатации Ignite (P2P официально рекомендован только для
// разработки).
//
// Коллокация по категории: ключ кэша — AffinityKey<Long>(id, category).
// GridCacheDefaultAffinityKeyMapper Ignite распознаёт тип AffinityKey
// специально и считает партицию ПО affinityKey() (category), а не по
// key() (id) — задокументированная механика ("Affinity Collocation" в
// доке Ignite), не открытие этого стенда. Эффект: все товары одной
// категории физически лежат на одном узле (одной партиции), что и делает
// TopByCategoryTask (Step 6) настоящим collocated-вычислением, а не
// вызовом "в пустоту".
import org.apache.ignite.Ignite;
import org.apache.ignite.IgniteCache;
import org.apache.ignite.Ignition;
import org.apache.ignite.cache.CacheMode;
import org.apache.ignite.cache.CachePeekMode;
import org.apache.ignite.cache.affinity.Affinity;
import org.apache.ignite.cache.affinity.AffinityKey;
import org.apache.ignite.cache.query.QueryCursor;
import org.apache.ignite.cache.query.ScanQuery;
import org.apache.ignite.client.ClientCache;
import org.apache.ignite.client.IgniteClient;
import org.apache.ignite.cluster.ClusterNode;
import org.apache.ignite.configuration.CacheConfiguration;
import org.apache.ignite.client.ClientCacheConfiguration;
import org.apache.ignite.configuration.ClientConfiguration;
import org.apache.ignite.configuration.IgniteConfiguration;
import org.apache.ignite.lang.IgniteBiPredicate;
import org.apache.ignite.lang.IgniteCallable;
import org.apache.ignite.spi.communication.tcp.TcpCommunicationSpi;
import org.apache.ignite.spi.discovery.tcp.TcpDiscoverySpi;
import org.apache.ignite.spi.discovery.tcp.ipfinder.vm.TcpDiscoveryVmIpFinder;

import javax.cache.Cache;
import java.io.Serializable;
import java.net.URI;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.Set;
import java.util.UUID;

public final class IgniteStand {

    static final String CACHE_NAME = "products";
    static final int BACKUPS = 1;
    // Проба фиксирована и совпадает с probeCategory в tarantool/main.go —
    // top_products_by_category(category, limit) сравнивается с этим же
    // стендом в Task 9 на одной и той же категории/семантике.
    static final String PROBE_CATEGORY = "tools";
    static final int TOP_LIMIT = 10;
    static final int LOAD_BATCH = 2000;

    public static void main(String[] args) throws Exception {
        String scenario = parseScenario(args);
        switch (scenario) {
            case "cache":
                scenarioCache();
                break;
            case "affinity":
                scenarioAffinity();
                break;
            case "compute":
                // -order — добавлено для Task 9 (бенчмарк "цена выноса вычислений
                // к данным", сценарий compute-locality): защита от смещения порядка
                // (naive выполняется первым vs compute первым) поверх УЖЕ
                // существовавшего сценария compute — ни TopByCategoryTask, ни
                // CategoryFilter, ни тай-брейк TOP_ORDER не тронуты, только замер
                // времени и порядок исполнения двух уже существовавших блоков.
                scenarioCompute(parseOrder(args));
                break;
            default:
                throw new IllegalArgumentException(
                        "неизвестный -scenario=" + scenario + " (ожидается cache|affinity|compute)");
        }
        System.out.println("OK: сценарий " + scenario + " завершён без ошибок");
    }

    private static String parseScenario(String[] args) {
        for (int i = 0; i < args.length; i++) {
            String a = args[i];
            if (a.startsWith("-scenario=")) {
                return a.substring("-scenario=".length());
            }
            if (a.equals("-scenario") && i + 1 < args.length) {
                return args[i + 1];
            }
        }
        throw new IllegalArgumentException("нужен -scenario=cache|affinity|compute");
    }

    // parseOrder — Task 9: naive-first (дефолт, соответствует порядку кода
    // scenarioCompute ДО этой задачи) или local-first (compute выполняется
    // первым). Используется только сценарием compute.
    private static String parseOrder(String[] args) {
        for (int i = 0; i < args.length; i++) {
            String a = args[i];
            if (a.startsWith("-order=")) {
                return a.substring("-order=".length());
            }
            if (a.equals("-order") && i + 1 < args.length) {
                return args[i + 1];
            }
        }
        return "naive-first";
    }

    private static String envOr(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isEmpty()) ? def : v;
    }

    // ---------- источник истины (PostgreSQL) ----------

    static final class Product implements Serializable {
        final long id;
        final String sku;
        final String title;
        final long priceCents;
        final String category;
        final long views;

        Product(long id, String sku, String title, long priceCents, String category, long views) {
            this.id = id;
            this.sku = sku;
            this.title = title;
            this.priceCents = priceCents;
            this.category = category;
            this.views = views;
        }
    }

    // Конвертация ORIGIN_DSN (Go-стиль postgres://user:pass@host:port/db?query,
    // единый env-контракт серии, см. README) в JDBC URL. Одна точка входа —
    // единственный формат DSN на все стенды серии.
    private static String[] jdbcFromDsn(String dsn) {
        URI u = URI.create(dsn);
        String user = "inmemory";
        String pass = "inmemory";
        String userInfo = u.getUserInfo();
        if (userInfo != null) {
            String[] parts = userInfo.split(":", 2);
            user = parts[0];
            if (parts.length > 1) {
                pass = parts[1];
            }
        }
        String query = u.getQuery() != null ? "?" + u.getQuery() : "";
        String jdbcUrl = "jdbc:postgresql://" + u.getHost() + ":" + u.getPort() + u.getPath() + query;
        return new String[]{jdbcUrl, user, pass};
    }

    // loadProductsFromPG — продукты + агрегированное число просмотров.
    // Тот же запрос (дословно), что в tarantool/main.go loadProductsFromPG —
    // общая семантика "views" для контрактной top_products_by_category.
    private static List<Product> loadProductsFromPG(String dsn) throws Exception {
        String[] jdbc = jdbcFromDsn(dsn);
        List<Product> out = new ArrayList<>(200_000);
        try (Connection conn = DriverManager.getConnection(jdbc[0], jdbc[1], jdbc[2]);
             Statement st = conn.createStatement();
             ResultSet rs = st.executeQuery(
                     "SELECT p.id, p.sku, p.title, p.price_cents, "
                             + "p.attrs->>'category' AS category, "
                             + "COALESCE(v.views, 0) AS views "
                             + "FROM products p "
                             + "LEFT JOIN (SELECT product_id, count(*) AS views FROM views GROUP BY product_id) v "
                             + "ON v.product_id = p.id "
                             + "ORDER BY p.id")) {
            while (rs.next()) {
                out.add(new Product(rs.getLong("id"), rs.getString("sku"), rs.getString("title"),
                        rs.getLong("price_cents"), rs.getString("category"), rs.getLong("views")));
            }
        }
        if (out.isEmpty()) {
            throw new AssertionError("АССЕРТ: PostgreSQL вернул 0 products — датасет не залит (dataset/ -load)");
        }
        return out;
    }

    // ---------- подключения ----------

    private static IgniteClient connectThin(String addrEnv) {
        ClientConfiguration cfg = new ClientConfiguration().setAddresses(addrEnv.split(","));
        return Ignition.startClient(cfg);
    }

    // Классическая клиент-нода — единственный способ получить Affinity API и
    // IgniteCompute.affinityCall в 2.18.0 (см. комментарий в шапке файла).
    // Тот же статический IP finder, что и у серверов (compose/ignite.yml,
    // ignite/config/default-config.xml) — иначе клиент-нода не найдёт кольцо.
    // peerClassLoadingEnabled НЕ выставляется (дефолт false) — должен строго
    // совпадать с серверами (Ignite отказывает в присоединении при
    // расхождении флага, живой прогон это подтвердил); классы стенда
    // (TopByCategoryTask/CategoryFilter) задеплоены на classpath серверов
    // заранее (см. compose/ignite.yml), P2P здесь не нужен.
    private static Ignite connectThick(String discoveryAddrEnv) {
        TcpDiscoveryVmIpFinder ipFinder = new TcpDiscoveryVmIpFinder();
        ipFinder.setAddresses(Arrays.asList(discoveryAddrEnv.split(",")));
        TcpDiscoverySpi discoSpi = new TcpDiscoverySpi().setIpFinder(ipFinder);
        IgniteConfiguration cfg = new IgniteConfiguration()
                .setIgniteInstanceName("inmemory-stand-client-" + UUID.randomUUID())
                .setClientMode(true)
                .setDiscoverySpi(discoSpi);
        return Ignition.start(cfg);
    }

    // ---------- загрузка кэша ----------

    private static ClientCache<AffinityKey<Long>, Product> ensureAndLoadThin(
            IgniteClient client, List<Product> rows) throws Exception {
        ClientCacheConfiguration ccfg = new ClientCacheConfiguration()
                .setName(CACHE_NAME).setCacheMode(CacheMode.PARTITIONED).setBackups(BACKUPS);
        ClientCache<AffinityKey<Long>, Product> cache = client.getOrCreateCache(ccfg);
        if (cache.size() == rows.size()) {
            return cache; // уже залито предыдущим прогоном — идемпотентно
        }
        Map<AffinityKey<Long>, Product> batch = new HashMap<>();
        for (Product p : rows) {
            batch.put(new AffinityKey<>(p.id, p.category), p);
            if (batch.size() >= LOAD_BATCH) {
                cache.putAll(batch);
                batch.clear();
            }
        }
        if (!batch.isEmpty()) {
            cache.putAll(batch);
        }
        return cache;
    }

    private static IgniteCache<AffinityKey<Long>, Product> ensureAndLoadThick(
            Ignite ignite, List<Product> rows) {
        CacheConfiguration<AffinityKey<Long>, Product> ccfg =
                new CacheConfiguration<AffinityKey<Long>, Product>(CACHE_NAME)
                        .setCacheMode(CacheMode.PARTITIONED)
                        .setBackups(BACKUPS);
        IgniteCache<AffinityKey<Long>, Product> cache = ignite.getOrCreateCache(ccfg);
        if (cache.size(CachePeekMode.PRIMARY) == rows.size()) {
            return cache;
        }
        Map<AffinityKey<Long>, Product> batch = new HashMap<>();
        for (Product p : rows) {
            batch.put(new AffinityKey<>(p.id, p.category), p);
            if (batch.size() >= LOAD_BATCH) {
                cache.putAll(batch);
                batch.clear();
            }
        }
        if (!batch.isEmpty()) {
            cache.putAll(batch);
        }
        return cache;
    }

    // ---------- Сценарий 1: cache (Step 4, тонкий клиент) ----------

    private static void scenarioCache() throws Exception {
        String dsn = envOr("ORIGIN_DSN", "postgres://inmemory:inmemory@127.0.0.1:5433/catalog?sslmode=disable");
        List<Product> rows = loadProductsFromPG(dsn);
        int loaded = rows.size();

        try (IgniteClient client = connectThin(envOr("IGNITE_ADDR", "127.0.0.1:10800"))) {
            ClientCache<AffinityKey<Long>, Product> cache = ensureAndLoadThin(client, rows);
            int size = cache.size();
            System.out.println("cache: тонкий клиент (Ignition.startClient), PARTITIONED cache, backups=" + BACKUPS);
            System.out.println("cache: залито из PostgreSQL=" + loaded + ", cache.size()=" + size);

            // Ассерт из брифа Step 4: размер кэша обязан совпасть с числом залитых товаров.
            if (size != loaded) {
                throw new AssertionError("АССЕРТ: в кэше " + size + ", залито " + loaded);
            }

            Random rnd = new Random(42);
            int probes = 50;
            int mismatches = 0;
            for (int i = 0; i < probes; i++) {
                Product want = rows.get(rnd.nextInt(rows.size()));
                Product got = cache.get(new AffinityKey<>(want.id, want.category));
                if (got == null || !got.title.equals(want.title) || got.priceCents != want.priceCents) {
                    mismatches++;
                }
            }
            System.out.println("cache: проверка чтения " + probes + " случайных ключей (seed=42), расхождений=" + mismatches);
            if (mismatches != 0) {
                throw new AssertionError("АССЕРТ: чтение из кэша разошлось с PostgreSQL на " + mismatches + " ключах");
            }
        }
    }

    // ---------- Сценарий 2: affinity (Step 5, толстая клиент-нода) ----------

    private static void scenarioAffinity() throws Exception {
        String dsn = envOr("ORIGIN_DSN", "postgres://inmemory:inmemory@127.0.0.1:5433/catalog?sslmode=disable");
        List<Product> rows = loadProductsFromPG(dsn);

        try (Ignite ignite = connectThick(envOr("IGNITE_DISCOVERY_ADDR", "ignite-1:47500..47509,ignite-2:47500..47509"))) {
            ensureAndLoadThick(ignite, rows);
            Affinity<AffinityKey<Long>> aff = ignite.affinity(CACHE_NAME);

            // Категории — из фактических данных, не хардкод дублирующий dataset/main.go.
            Map<String, Long> catCounts = new LinkedHashMap<>();
            for (Product p : rows) {
                catCounts.merge(p.category, 1L, Long::sum);
            }
            List<String> categories = new ArrayList<>(catCounts.keySet());
            categories.sort(Comparator.naturalOrder());

            // AffinityKeyMapper для AffinityKey учитывает ТОЛЬКО affinityKey()
            // (category), значение key() (id) на маршрутизацию не влияет —
            // поэтому id=0L ниже безразличен, важна только category.
            Map<String, ClusterNode> catNode = new LinkedHashMap<>();
            for (String cat : categories) {
                catNode.put(cat, aff.mapKeyToNode(new AffinityKey<>(0L, cat)));
            }

            Set<UUID> nodesUsed = new LinkedHashSet<>();
            Map<UUID, Long> perNodeCount = new LinkedHashMap<>();
            Map<UUID, String> nodeLabel = new LinkedHashMap<>();
            System.out.println("affinity: коллокация категорий по узлам (affinityKey=category)");
            for (String cat : categories) {
                ClusterNode node = catNode.get(cat);
                nodesUsed.add(node.id());
                nodeLabel.put(node.id(), shortNode(node));
                long cnt = catCounts.get(cat);
                perNodeCount.merge(node.id(), cnt, Long::sum);
                System.out.printf("  category=%-8s -> node %s   products=%d%n", cat, shortNode(node), cnt);
            }

            System.out.println("affinity: распределение ключей по узлам (итог по всем " + rows.size() + " продуктам)");
            for (Map.Entry<UUID, Long> e : perNodeCount.entrySet()) {
                System.out.printf("  node %s -> %d ключей%n", nodeLabel.get(e.getKey()), e.getValue());
            }

            // Ассерт из брифа Step 5: ключи обязаны распределиться по ОБОИМ узлам.
            if (nodesUsed.size() < 2) {
                throw new AssertionError("АССЕРТ: данные легли на " + nodesUsed.size() + " узел — распределения нет");
            }
        }
    }

    private static String shortNode(ClusterNode node) {
        return node.id().toString().substring(0, 8) + " (" + node.consistentId() + ")";
    }

    // ---------- Сценарий 3: compute (Step 6, толстая клиент-нода) ----------

    // TopByCategoryTask — контракт для Task 9 (бенчмарк "цена выноса
    // вычислений к данным"): affinity-задача, считающая топ товаров категории
    // НА узле, где физически лежит партиция этой категории. Симметрична
    // top_products_by_category(category, limit) в tarantool/init.lua —
    // одинаковая семантика (топ по views, тай-брейк ниже), разный движок.
    static final class TopByCategoryTask implements IgniteCallable<List<TopEntry>> {
        private final String cacheName;
        private final String category;
        private final int limit;

        TopByCategoryTask(String cacheName, String category, int limit) {
            this.cacheName = cacheName;
            this.category = category;
            this.limit = limit;
        }

        @Override
        public List<TopEntry> call() {
            // Ignition.localIgnite() — доступ к Ignite-инстансу УЗЛА, на
            // котором сейчас исполняется job (это и есть "compute к данным":
            // задача приехала на сервер по сети, а не данные — к задаче).
            Ignite local = Ignition.localIgnite();
            IgniteCache<AffinityKey<Long>, Product> cache = local.cache(cacheName);
            List<TopEntry> all = new ArrayList<>();
            // CachePeekMode.PRIMARY — только первичные копии партиций этого
            // узла; бэкапы (backups=1) не в счёт, иначе задвоили бы записи.
            for (Cache.Entry<AffinityKey<Long>, Product> e : cache.localEntries(CachePeekMode.PRIMARY)) {
                Product p = e.getValue();
                if (p.category.equals(category)) {
                    all.add(new TopEntry(p.id, p.title, p.views));
                }
            }
            all.sort(TOP_ORDER);
            return all.size() > limit ? new ArrayList<>(all.subList(0, limit)) : all;
        }
    }

    // Тай-брейк для равных views — DESC по views, затем ASC по id
    // (детерминированно). Это НЕЗАВИСИМЫЙ от Tarantool выбор: контрактная
    // top_products_by_category в tarantool/init.lua тай-брейкает по PK
    // DESCENDING — задокументированный артефакт её REQ-итератора по
    // неуникальному вторичному индексу (см. tarantool/main.go, комментарий
    // над clientTopByCategory, и task-3-report.md). Здесь тай-брейк выбран
    // заново и не обязан совпадать — важно только внутреннее согласие между
    // naive и compute вариантами ЭТОГО стенда (ассерт ниже). Сверка
    // семантики между Ignite и Tarantool — задача Task 9, не этой.
    private static final Comparator<TopEntry> TOP_ORDER =
            Comparator.comparingLong((TopEntry t) -> t.views).reversed()
                    .thenComparingLong(t -> t.id);

    static final class TopEntry implements Serializable {
        final long id;
        final String title;
        final long views;

        TopEntry(long id, String title, long views) {
            this.id = id;
            this.title = title;
            this.views = views;
        }

        @Override
        public boolean equals(Object o) {
            if (this == o) return true;
            if (!(o instanceof TopEntry)) return false;
            TopEntry t = (TopEntry) o;
            return id == t.id && views == t.views && title.equals(t.title);
        }

        @Override
        public int hashCode() {
            return Long.hashCode(id) * 31 + title.hashCode();
        }

        @Override
        public String toString() {
            return "id=" + id + " title=" + title + " views=" + views;
        }
    }

    // CategoryFilter — предикат ScanQuery для наивного варианта: вычитать
    // ВСЮ категорию в клиент (со всех узлов, обе партиции) и посчитать топ
    // там же. Явный serializable-класс (не лямбда) — предикат уезжает на
    // сервер по сети вместе с запросом.
    static final class CategoryFilter implements IgniteBiPredicate<AffinityKey<Long>, Product> {
        private final String category;

        CategoryFilter(String category) {
            this.category = category;
        }

        @Override
        public boolean apply(AffinityKey<Long> key, Product val) {
            return val.category.equals(category);
        }
    }

    // median — Task 9, см. комментарий над TIMING_REPS в scenarioCompute.
    private static long median(long[] arr) {
        long[] sorted = arr.clone();
        Arrays.sort(sorted);
        return sorted[sorted.length / 2];
    }

    // ByteStats/byteStats — L2 (внешнее ревью 18.07): та же схема "медиана +
    // диапазон из TIMING_REPS повторов", что и median() для времени выше,
    // применённая к дельтам байт TcpCommunicationSpi вокруг каждого отдельного
    // вызова (см. runNaive/runCompute в scenarioCompute).
    static final class ByteStats {
        final long median;
        final long lo;
        final long hi;

        ByteStats(long median, long lo, long hi) {
            this.median = median;
            this.lo = lo;
            this.hi = hi;
        }
    }

    private static ByteStats byteStats(long[] arr) {
        long[] sorted = arr.clone();
        Arrays.sort(sorted);
        return new ByteStats(sorted[sorted.length / 2], sorted[0], sorted[sorted.length - 1]);
    }

    // scenarioCompute — order: "naive-first" (дефолт, исходный порядок кода
    // Стенда 6) или "local-first" (compute выполняется до naive). Добавлено
    // Task 9 для защиты от смещения порядка (см. tarantool-эквивалент в
    // benchmark/main.go, тот же приём). Публикуемые числа (naiveOverNetwork/
    // computeOverNetwork/топы/ассерты) от порядка НЕ зависят — меняется
    // только то, какой блок замеряется на "холодном" пути первым.
    private static void scenarioCompute(String order) throws Exception {
        if (!order.equals("naive-first") && !order.equals("local-first")) {
            throw new IllegalArgumentException("неизвестный -order=" + order + " (ожидается naive-first|local-first)");
        }
        String dsn = envOr("ORIGIN_DSN", "postgres://inmemory:inmemory@127.0.0.1:5433/catalog?sslmode=disable");
        List<Product> rows = loadProductsFromPG(dsn);
        long categorySizePG = rows.stream().filter(p -> p.category.equals(PROBE_CATEGORY)).count();

        try (Ignite ignite = connectThick(envOr("IGNITE_DISCOVERY_ADDR", "ignite-1:47500..47509,ignite-2:47500..47509"))) {
            IgniteCache<AffinityKey<Long>, Product> cache = ensureAndLoadThick(ignite, rows);

            // ---------- L2 (внешнее ревью 18.07): сокетные байты через TcpCommunicationSpi ----------
            // getSentBytesCount()/getReceivedBytesCount() — штатные метрики
            // TcpCommunicationSpi (javap ignite-core-2.18.0.jar подтвердил оба
            // метода живьём, не выдумано). connectThick поднимает классическую
            // client-ноду — полноправного участника communication SPI (тот же
            // канал, по которому едут ScanQuery-результаты и affinityCall), в
            // отличие от тонкого клиента (сценарий cache), у которого этого SPI
            // нет вовсе. getCommunicationSpi() ничего явно не настраивает в
            // connectThick — если Ignite не резолвит SPI по умолчанию в
            // TcpCommunicationSpi к моменту старта, ниже это честно
            // зафиксировано ПРЕДУПРЕЖДЕНИЕМ, а не тихой заменой на 0.
            TcpCommunicationSpi commSpi = null;
            Object commSpiCandidate = ignite.configuration().getCommunicationSpi();
            if (commSpiCandidate instanceof TcpCommunicationSpi) {
                commSpi = (TcpCommunicationSpi) commSpiCandidate;
            } else {
                System.out.println("compute: ПРЕДУПРЕЖДЕНИЕ — TcpCommunicationSpi недоступен (getCommunicationSpi()="
                        + (commSpiCandidate == null ? "null" : commSpiCandidate.getClass().getName())
                        + "), байты comm SPI для ignite в этом прогоне НЕ измерены");
            }
            final TcpCommunicationSpi commSpiFinal = commSpi;

            // Прогрев (тот же живой урок, что и в Tarantool-эквиваленте
            // benchmark/main.go, см. комментарий там): КАЖДЫЙ подпроцесс здесь
            // — свежая JVM (docker run порождает её заново на каждый вызов
            // сценария), первый вызов ScanQuery/affinityCall в свежей JVM
            // систематически медленнее любого следующего (JIT/подключение к
            // кластеру ещё не прогреты) — это не свойство naive-vs-compute, а
            // холодный старт процесса. Один непомеренный вызов каждого пути
            // ДО измеряемого блока устраняет эту асимметрию независимо от
            // того, какой из двух путей окажется первым по -order.
            try (QueryCursor<Cache.Entry<AffinityKey<Long>, Product>> warmCur =
                         cache.query(new ScanQuery<>(new CategoryFilter(PROBE_CATEGORY)))) {
                for (Cache.Entry<AffinityKey<Long>, Product> ignored : warmCur) {
                    // прогрев, результат не используется
                }
            }
            ignite.compute().affinityCall(
                    CACHE_NAME, PROBE_CATEGORY, new TopByCategoryTask(CACHE_NAME, PROBE_CATEGORY, TOP_LIMIT));

            List<TopEntry> naiveAll = new ArrayList<>();
            int[] naiveOverNetworkBox = new int[1];
            List<TopEntry>[] clientTopBox = new List[1];
            List<TopEntry>[] computeTopBox = new List[1];
            int[] computeOverNetworkBox = new int[1];
            long[] naiveElapsedNsBox = new long[1];
            long[] computeElapsedNsBox = new long[1];
            ByteStats[] naiveSentBox = new ByteStats[1];
            ByteStats[] naiveRecvBox = new ByteStats[1];
            ByteStats[] computeSentBox = new ByteStats[1];
            ByteStats[] computeRecvBox = new ByteStats[1];

            // TIMING_REPS/median — тот же приём, что в Tarantool-эквиваленте
            // (benchmark/main.go, clTimingReps). Диагностика поведения
            // одиночных замеров ("прыгающий" ratio между порядками, ложные
            // срабатывания ассерта) относится к Tarantool-стенду; для Ignite
            // такое поведение отдельно НЕ измерялось и здесь не утверждается.
            // Соотношение джиттера платформы и полезного сигнала на этом
            // стенде тоже не измерялось — оценки его масштаба здесь нет.
            //
            // О силе утверждения — аккуратно. Бимодальность задержки
            // наблюдалась на Tarantool-стороне, в ОТДЕЛЬНОЙ диагностической
            // пробе, которая не сохранена; для Ignite форма распределения
            // отдельно НЕ измерялась вовсе. Поэтому здесь не утверждается ни
            // что распределение бимодально, ни что медиана этот эффект
            // "подавляет". N=51 взят по двум причинам: симметрия методики с
            // Tarantool-стендом (сравнивать надо одинаково измеренное) и
            // снижение чувствительности итога к одному случайному отсчёту.
            // Платформенную нестабильность это не устраняет.
            //
            // Топ и число объектов по сети берутся с ПОСЛЕДНЕГО повтора —
            // они детерминированы и от повтора к повтору не меняются.
            final int TIMING_REPS = 51;

            Runnable runNaive = () -> {
                long[] durs = new long[TIMING_REPS];
                long[] sentDeltas = new long[TIMING_REPS];
                long[] recvDeltas = new long[TIMING_REPS];
                List<TopEntry> topResult = null;
                for (int rep = 0; rep < TIMING_REPS; rep++) {
                    naiveAll.clear();
                    long s0 = commSpiFinal != null ? commSpiFinal.getSentBytesCount() : 0L;
                    long r0 = commSpiFinal != null ? commSpiFinal.getReceivedBytesCount() : 0L;
                    long t0 = System.nanoTime();
                    // Наивный вариант: ScanQuery со всей категории в клиент, топ считает клиент.
                    try (QueryCursor<Cache.Entry<AffinityKey<Long>, Product>> cur =
                                 cache.query(new ScanQuery<>(new CategoryFilter(PROBE_CATEGORY)))) {
                        for (Cache.Entry<AffinityKey<Long>, Product> e : cur) {
                            Product p = e.getValue();
                            naiveAll.add(new TopEntry(p.id, p.title, p.views));
                        }
                    }
                    // ВТОРОЙ РАУНД ВНЕШНЕГО РЕВЬЮ (18.07, замечание 1 — "Ignite
                    // считает байты не строго вокруг сетевой операции"): конечный
                    // snapshot счётчиков TcpCommunicationSpi снимается ЗДЕСЬ —
                    // сразу после закрытия cursor (try-with-resources выше уже
                    // отработал), ДО naiveAll.sort()/копирования topResult. Раньше
                    // snapshot стоял ниже, за сортировкой и List-копией: обе —
                    // чистый CPU, без сети, но занимали время ВНУТРИ измеряемого
                    // байтового интервала, а TcpCommunicationSpi — счётчик всей
                    // client-node (см. комментарий у commSpiFinal выше), а не
                    // этого конкретного вызова, — в интервал мог попасть
                    // посторонний фоновый трафик той же ноды (discovery/
                    // heartbeat служебные сообщения SPI). На миллионах байт
                    // ScanQuery-результата эффект тонул в шуме, но на голых
                    // единицах-десятках байт (см. FIXTURES.md, compute-путь)
                    // он мог доминировать в опубликованном числе.
                    long s1 = commSpiFinal != null ? commSpiFinal.getSentBytesCount() : 0L;
                    long r1 = commSpiFinal != null ? commSpiFinal.getReceivedBytesCount() : 0L;
                    // ПОСЛЕ РЕВЬЮ (P1, см. task-9-report.md, "Фикс таймера наивного
                    // пути"): сортировка и отбор top-N раньше стояли ЗА пределами
                    // измеряемого участка (после runNaive.run()/runCompute.run()) —
                    // наивный путь получался недомерен, он не платил за работу,
                    // которая ему объективно нужна для получения того же результата
                    // (топ-N), что и affinity-задача. Compute-вариант уже возвращает
                    // готовый топ end-to-end, его не трогаем. Сортировка теперь внутри
                    // цикла, до остановки таймера — наивный путь end-to-end (elapsed
                    // ЕЁ включает намеренно), но байтовый интервал (s0..s1/r0..r1)
                    // сортировку уже не захватывает — сортировка байт не расходует.
                    naiveAll.sort(TOP_ORDER);
                    topResult = new ArrayList<>(naiveAll.subList(0, Math.min(TOP_LIMIT, naiveAll.size())));
                    durs[rep] = System.nanoTime() - t0;
                    if (commSpiFinal != null) {
                        sentDeltas[rep] = s1 - s0;
                        recvDeltas[rep] = r1 - r0;
                    }
                }
                naiveElapsedNsBox[0] = median(durs);
                naiveOverNetworkBox[0] = naiveAll.size();
                clientTopBox[0] = topResult;
                if (commSpiFinal != null) {
                    naiveSentBox[0] = byteStats(sentDeltas);
                    naiveRecvBox[0] = byteStats(recvDeltas);
                }
            };
            Runnable runCompute = () -> {
                long[] durs = new long[TIMING_REPS];
                long[] sentDeltas = new long[TIMING_REPS];
                long[] recvDeltas = new long[TIMING_REPS];
                List<TopEntry> computeTop = null;
                for (int rep = 0; rep < TIMING_REPS; rep++) {
                    long s0 = commSpiFinal != null ? commSpiFinal.getSentBytesCount() : 0L;
                    long r0 = commSpiFinal != null ? commSpiFinal.getReceivedBytesCount() : 0L;
                    long t0 = System.nanoTime();
                    // Compute-вариант: affinity-задача исполняется НА узле категории,
                    // по сети едет только результат (TOP_LIMIT записей).
                    computeTop = ignite.compute().affinityCall(
                            CACHE_NAME, PROBE_CATEGORY, new TopByCategoryTask(CACHE_NAME, PROBE_CATEGORY, TOP_LIMIT));
                    // ВТОРОЙ РАУНД ВНЕШНЕГО РЕВЬЮ (18.07, замечание 1, проверка для
                    // compute-ветки): здесь и без правки байтовый интервал уже
                    // минимален — affinityCall блокирующий и возвращает готовый
                    // результат, снимок счётчиков сразу за ним не захватывает ничего
                    // несетевого (в отличие от наивной ветки выше, где раньше между
                    // закрытием cursor и снимком стояли sort()/List-копия). Править
                    // здесь нечего, оставлено намеренно как есть.
                    durs[rep] = System.nanoTime() - t0;
                    if (commSpiFinal != null) {
                        sentDeltas[rep] = commSpiFinal.getSentBytesCount() - s0;
                        recvDeltas[rep] = commSpiFinal.getReceivedBytesCount() - r0;
                    }
                }
                computeElapsedNsBox[0] = median(durs);
                computeTopBox[0] = computeTop;
                computeOverNetworkBox[0] = computeTop.size();
                if (commSpiFinal != null) {
                    computeSentBox[0] = byteStats(sentDeltas);
                    computeRecvBox[0] = byteStats(recvDeltas);
                }
            };

            if (order.equals("naive-first")) {
                runNaive.run();
                runCompute.run();
            } else {
                runCompute.run();
                runNaive.run();
            }

            int naiveOverNetwork = naiveOverNetworkBox[0];
            List<TopEntry> clientTop = clientTopBox[0];
            List<TopEntry> computeTop = computeTopBox[0];
            int computeOverNetwork = computeOverNetworkBox[0];
            double naiveMs = naiveElapsedNsBox[0] / 1_000_000.0;
            double computeMs = computeElapsedNsBox[0] / 1_000_000.0;

            System.out.println("compute: category=" + PROBE_CATEGORY + " limit=" + TOP_LIMIT + " order=" + order);
            System.out.println("compute: товаров в категории (PostgreSQL, источник истины)=" + categorySizePG);
            System.out.println("compute: товаров в категории (ScanQuery по кэшу)=" + naiveOverNetwork);
            System.out.println("compute: объектов уехало по сети — наивно=" + naiveOverNetwork
                    + ", compute=" + computeOverNetwork
                    + " (в " + String.format("%.1f", naiveOverNetwork / (double) computeOverNetwork) + "x меньше)");
            // Абсолютное время — только для вычисления отношения внутри этого
            // прогона (см. README "Границы метода"): Docker Desktop даёт
            // непредсказуемый оверхед, абсолютные мс не публикуются как факт о
            // системе.
            System.out.println("compute: время_наивно_мс=" + String.format("%.3f", naiveMs)
                    + " время_compute_мс=" + String.format("%.3f", computeMs)
                    + " ratio_naive_over_compute=" + String.format("%.3f", naiveMs / computeMs));

            // БАЙТЫ ПО СЕТИ (L2, внешнее ревью 18.07) — настоящий сокетный
            // трафик communication SPI classic client-node, не число объектов
            // (см. комментарий у commSpiFinal выше). Формат строки ниже
            // разобран регуляркой в benchmark/main.go (clReCommBytes) — менять
            // только синхронно с ней.
            //
            // ВТОРОЙ РАУНД ВНЕШНЕГО РЕВЬЮ (18.07, замечание 1) — важная
            // честная оговорка: даже после сужения байтового интервала до
            // "s0 сразу перед вызовом .. s1/s0 сразу после" (см. runNaive/
            // runCompute выше), TcpCommunicationSpi остаётся счётчиком ВСЕЙ
            // client-node, а не этого конкретного вызова. Если между s0 и s1
            // тот же процесс успел получить фоновое сообщение SPI (discovery
            // heartbeat, метаданные кластера) — оно попадёт в дельту. Это НЕ
            // исключено методом измерения, а лишь МИНИМИЗИРОВАНО сужением
            // интервала до границ одного вызова. На миллионах байт ScanQuery
            // это тонет в шуме; на голых единицах-десятках байт (compute-путь)
            // фоновый трафик может быть заметной долей опубликованного числа
            // — см. ту же оговорку в FIXTURES.md.
            if (commSpiFinal != null) {
                ByteStats ns = naiveSentBox[0];
                ByteStats nr = naiveRecvBox[0];
                ByteStats cs = computeSentBox[0];
                ByteStats cr = computeRecvBox[0];
                System.out.println("compute: байты_comm_spi_медиана наивно_sent=" + ns.median
                        + " наивно_received=" + nr.median
                        + " compute_sent=" + cs.median
                        + " compute_received=" + cr.median);
                System.out.println("compute: байты_comm_spi_диапазон наивно_sent=[" + ns.lo + ".." + ns.hi + "]"
                        + " наивно_received=[" + nr.lo + ".." + nr.hi + "]"
                        + " compute_sent=[" + cs.lo + ".." + cs.hi + "]"
                        + " compute_received=[" + cr.lo + ".." + cr.hi + "]");
                long naiveTotal = ns.median + nr.median;
                long computeTotal = cs.median + cr.median;
                System.out.println("compute: байты_comm_spi суммарно(sent+received) — наивно=" + naiveTotal
                        + ", compute=" + computeTotal
                        + " (в " + String.format("%.1f", naiveTotal / (double) computeTotal) + "x меньше)");
            }

            System.out.println("compute: топ клиента (naive)");
            for (int i = 0; i < clientTop.size(); i++) {
                System.out.println("  #" + (i + 1) + " " + clientTop.get(i));
            }
            System.out.println("compute: топ affinity-задачи (TopByCategoryTask)");
            for (int i = 0; i < computeTop.size(); i++) {
                System.out.println("  #" + (i + 1) + " " + computeTop.get(i));
            }

            // Ассерт-сверка: ScanQuery не должен задваивать записи (бэкап +
            // primary) — иначе naiveOverNetwork не значит то, что заявлено.
            if (naiveOverNetwork != categorySizePG) {
                throw new AssertionError("АССЕРТ: ScanQuery вернул " + naiveOverNetwork
                        + " записей категории " + PROBE_CATEGORY + ", в PostgreSQL " + categorySizePG
                        + " — возможное задвоение бэкапами или неполная заливка");
            }

            // Ассерт из брифа Step 6: оба варианта обязаны дать одинаковый топ.
            if (!computeTop.equals(clientTop)) {
                throw new AssertionError("АССЕРТ: топ compute != топ клиента:\n" + computeTop + "\n" + clientTop);
            }
        }
    }
}
