package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/**
 * Проба: одно измерение «что будет, если запись, сделанную по одной
 * версии схемы, прочитать по другой».
 *
 * Принимает КООРДИНАТЫ КЛЕТКИ и ничего больше. Ни путей, ни записей, ни
 * ожидания аргументом нет: пока данные приходили снаружи, любую клетку
 * можно было перевести в любой исход подбором записи.
 */
public final class Probe {

    /** Литерал, по которому делятся потоки результатов двух реализаций. */
    private static final String LANG = "java";

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static final Set<String> FORMATS = Set.of("json", "json-schema", "avro", "protobuf");
    // alias_conflict и retype_message — круг правок (задача 6bis), см.
    // stand.Changes в Go-части для полного обоснования.
    private static final Set<String> CHANGES = new LinkedHashSet<>(List.of(
            "base", "add_default", "add_nodefault", "remove", "rename",
            "retype", "reuse_tag", "unknown_field",
            "alias_conflict", "retype_message"));
    private static final Set<String> DIRECTIONS = Set.of("same", "newer_reader", "newer_writer");
    // cross-emit/cross-accept/identity — Задача 8, ось перекрёстного
    // чтения (spec.md §17): cross-emit отдаёт байты через файл обмена,
    // cross-accept принимает чужие и классифицирует их ТОЙ ЖЕ функцией
    // classify(), которой пользуется compat(); identity — контроль
    // байтовой идентичности одной реализации с собой (§17.6).
    private static final Set<String> OPS = Set.of(
            "compat", "roundtrip", "size", "cross-emit", "cross-accept", "identity");
    private static final Set<String> WRITER_LANGS = Set.of("go", "java");

    private Probe() {}

    public static void main(String[] args) {
        System.exit(run(args,
                new PrintStream(System.out, true, StandardCharsets.UTF_8),
                new PrintStream(System.err, true, StandardCharsets.UTF_8)));
    }

    /**
     * @return 0 — все строки клетки напечатаны (любые исходы, включая
     * refused, error и n/a, это результат, а не сбой); не 0 — отказ пробы,
     * при котором не печатается НИ ОДНОЙ строки. Наполовину напечатанная
     * клетка хуже ненапечатанной: по ней нельзя понять, чего не хватает,
     * поэтому строки копятся и выводятся только целиком.
     */
    static int run(String[] args, PrintStream out, PrintStream err) {
        // "." — рабочий каталог ПРОЦЕССА: настоящий CLI-вызов запускает
        // отдельную JVM на каждую координату (см. bench/run-cross.sh),
        // поэтому это ровно тот же каталог, откуда его запустил
        // вызывающий сценарий (spec.md §17.2). Тестовый оверлоад ниже
        // передаёт временный каталог явно — внутри одного тестового
        // процесса JVM не «перейти» в другой каталог, а гонять пробу как
        // внешний процесс ради одного этого не стоит.
        return run(args, out, err, Path.of("."));
    }

    static int run(String[] args, PrintStream out, PrintStream err, Path exchangeDir) {
        List<ObjectNode> rows;
        try {
            rows = build(args, exchangeDir);
        } catch (Failures.ProbeRefusal e) {
            err.println("отказ пробы: " + e.getMessage());
            return 1;
        }
        for (ObjectNode row : rows) {
            out.println(row.toString());
        }
        return 0;
    }

    private static List<ObjectNode> build(String[] args, Path exchangeDir) {
        Args a = Args.parse(args);

        Stand stand = Stand.locate();
        String notation = Stand.notationOf(a.format);

        // Версии и изменение — из направления и изменения.
        Stand.SchemaFile writerFile;
        Stand.SchemaFile readerFile;
        switch (a.direction) {
            case "newer_reader" -> {
                writerFile = stand.schema(notation, 1, "");
                readerFile = stand.schema(notation, 2, a.change);
            }
            case "newer_writer" -> {
                writerFile = stand.schema(notation, 2, a.change);
                readerFile = stand.schema(notation, 1, "");
            }
            default -> {
                // Направление same: обе схемы — ОДНА И ТА ЖЕ запись
                // манифеста, поэтому файл читается один раз.
                writerFile = a.change.equals("base")
                        ? stand.schema(notation, 1, "")
                        : stand.schema(notation, 2, a.change);
                readerFile = writerFile;
            }
        }

        List<Map<String, Value>> records = stand.records(writerFile.version(), writerFile.change());

        // Причина n/a № 1: круговая проба у плеча без непрозрачного
        // остатка. Определяется одним лишь плечом, до всякого разбора
        // схем, и по правилу приоритета идёт первой.
        if (a.op.equals("roundtrip") && !a.format.equals("protobuf")) {
            return notApplicable(a, records.size(),
                    "круговая проба: у плеча " + a.format + " нет непрозрачного остатка");
        }

        SchemaModel writer;
        SchemaModel reader;
        Codec codec;
        try {
            writer = SchemaModel.parse(writerFile);
            reader = writerFile == readerFile ? writer : SchemaModel.parse(readerFile);
            codec = Codec.of(a.format, writer, reader);
        } catch (Failures.SchemaSetup e) {
            // Сбой подготовки схемы — сломалась ПРОБА, а не формат.
            return errorRows(a, records.size(), e.getMessage());
        }

        // Причина n/a № 2: заявленное изменение в этой нотации не
        // выражается никак. Проверяется ДО того, как понадобятся записи:
        // у вырожденной пары запись писателя заведомо не соответствует
        // схеме читателя, и обратный порядок превратил бы находку стенда
        // в его поломку.
        if (writerFile != readerFile && writer.structurallySameAs(reader)) {
            return notApplicable(a, records.size(),
                    "изменение " + a.change + " в нотации " + notation + " не выражается: "
                            + "наборы полей схем писателя и читателя структурно совпадают");
        }

        // Соответствие записей схеме писателя — отказ пробы, а не строка
        // таблицы, и правило одинаково для обычной, круговой и размерной
        // пробы.
        for (int i = 0; i < records.size(); i++) {
            Expect.checkRecordAgainstWriter(writer, reader, records.get(i), i);
        }

        if (a.op.equals("size")) {
            return sizeRows(a, records, writer, writerFile, codec);
        }

        // Задача 8, ось перекрёстного чтения (spec.md §17). Три новых
        // вида пробы попадают сюда ПОСЛЕ подготовки схем и проверки
        // вырожденности — те же причины n/a/error, что и у
        // compat/roundtrip, обязаны сработать одинаково: клетка,
        // невыразимая в этой нотации, не становится выразимой оттого,
        // что байты идут через файл, а не напрямую.
        if (a.op.equals("cross-emit")) {
            return crossEmitRows(a, records, codec, exchangeDir);
        }
        if (a.op.equals("cross-accept")) {
            return crossAcceptRows(a, records, writer, reader, codec, exchangeDir);
        }
        if (a.op.equals("identity")) {
            return identityRows(a, records, codec);
        }

        List<ObjectNode> rows = new ArrayList<>();
        for (int i = 0; i < records.size(); i++) {
            rows.add(a.op.equals("roundtrip")
                    ? roundtrip(a, i, records.get(i), writer, reader, codec)
                    : compat(a, i, records.get(i), writer, reader, codec));
        }
        return rows;
    }

    // -------------------------------------------------- --op=size (Задача 5)

    /**
     * Уровень сжатия зафиксирован ради воспроизводимости числа: без
     * фиксации сжатый размер зависел бы от версии библиотеки/дефолта, а
     * не от формата. 3 — обычный уровень «по умолчанию» у zstd, тот, что
     * используют, когда явно не просят другого. Go-часть обязана
     * использовать тот же уровень — см. zstdLevel в cmd/probe/main.go и
     * обоснование в spec.md.
     */
    private static final int ZSTD_LEVEL = 3;

    /**
     * Строит строки вида size: bytes — размер закодированной записи,
     * zstd — размер ТЕХ ЖЕ БАЙТОВ под сжатием, schema_bytes — вес
     * КАНОНИЧЕСКОЙ формы схемы (см. canonicalWeight), schema_file_bytes —
     * вес файла схемы КАК ЕСТЬ (для сравнения — во что превращается
     * канонизация), batch_bytes/batch_zstd — размер и сжатие ПАЧКИ из
     * всех пяти записей клетки (spec.md §10.3.2).
     *
     * Круг ревью 2, находка C2: вес схемы «как есть» смешивает три
     * разные единицы — размеченный отступами текст (Avro, JSON Schema)
     * против компилированного двоичного файла (Protobuf), и Avro/JSON
     * Schema платят «налог форматирования», которого Protobuf не платит
     * по устройству. Основное число (schema_bytes) — каноническая
     * форма, снимающая эту разницу; raw-вес остаётся в строке отдельным
     * полем, чтобы было видно, во сколько обходится форматирование.
     *
     * Форма строки не совпадает с compat/roundtrip: у оси размера нет
     * ни исхода, ни стадии, ни ожидания — это измерение одной схемы, а
     * не сравнение писателя с читателем.
     */
    private static List<ObjectNode> sizeRows(Args a, List<Map<String, Value>> records,
                                              SchemaModel writer, Stand.SchemaFile writerFile,
                                              Codec codec) {
        // Контрольное плечо схему не читает вовсе: читателю она не нужна
        // ни для чего, и вес схемы у него — нуль в обоих измерениях.
        int schemaFileBytes = a.format.equals("json") ? 0 : writerFile.bytes().length;
        int schemaBytes = a.format.equals("json") ? 0 : canonicalWeight(a.format, writer, writerFile);

        // Каждая запись кодируется РОВНО ОДИН РАЗ: эти же байты идут и в
        // bytes/zstd отдельной строки, и в пачку — повторное кодирование
        // для пачки означало бы мерить что-то другое.
        byte[][] encoded = new byte[records.size()][];
        for (int i = 0; i < records.size(); i++) {
            try {
                encoded[i] = codec.encode(records.get(i));
            } catch (Failures.FormatRefusal e) {
                // Канонические записи по построению проходят §8.1: отказ
                // здесь означал бы, что сломалась проба, а не формат — у
                // оси размера нет исхода, чтобы напечатать его строкой.
                throw new Failures.ProbeRefusal(
                        "--op=size: " + a.format + " отказался закодировать каноническую запись " + i
                                + ": " + e.getMessage());
            }
        }

        // batch_bytes/batch_zstd/batch_hash — свойства КЛЕТКИ, а не
        // отдельной записи: считаются один раз и печатаются одним и тем
        // же значением на всех пяти строках.
        byte[] batch = frameBatch(encoded);
        int batchBytes = batch.length;
        int batchZstd = com.github.luben.zstd.Zstd.compress(batch, ZSTD_LEVEL).length;
        // Круг ревью 2, находка M1: длина пачки не ловит расхождение
        // СОДЕРЖИМОГО при той же длине — ровно то, что даёт
        // недетерминированный порядок полей у protobuf (находка C3).
        // Хеш содержимого делает межъязыковое «совпало побайтово»
        // проверяемым утверждением, а не «совпали длины».
        String batchHash = sha256Hex(batch);

        // Круг ревью 3: у JSON, как и у Protobuf, нет канонической формы
        // БАЙТ, определённой спецификацией формата — порядок ключей
        // объекта в JSON вообще не специфицирован, и обе реализации
        // вправе выбрать свой (одна сортирует по алфавиту, другая
        // сохраняет порядок вставки — обе правы). BatchHash поэтому не
        // годится в качестве проверки для всех плеч; batch_content_hash
        // расшифровывает каждую запись обратно ТОЙ ЖЕ схемой
        // (direction=same — чистый круговой прогон без переименований и
        // приведений типа) и хеширует канонический вид результата (ключи
        // объекта — по алфавиту, TreeMap), а не байты с провода.
        ArrayNode decodedArray = MAPPER.createArrayNode();
        for (int i = 0; i < records.size(); i++) {
            Map<String, Value> decoded;
            try {
                decoded = codec.decode(encoded[i]);
            } catch (Failures.FormatRefusal e) {
                throw new Failures.ProbeRefusal(
                        "--op=size: " + a.format + " отказался расшифровать обратно запись " + i
                                + " для сверки содержимого: " + e.getMessage());
            }
            decodedArray.add(toCanonicalJson(decoded));
        }
        byte[] canonicalContent;
        try {
            canonicalContent = MAPPER.writeValueAsBytes(decodedArray);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
        String batchContentHash = sha256Hex(canonicalContent);

        List<ObjectNode> rows = new ArrayList<>();
        for (int i = 0; i < records.size(); i++) {
            // zstd сжимает РОВНО ТЕ БАЙТЫ, что попали в bytes выше, — не
            // запись, перекодированную ещё раз каким-то другим путём.
            int zstdBytes = com.github.luben.zstd.Zstd.compress(encoded[i], ZSTD_LEVEL).length;
            rows.add(sizeRow(a, i, records.get(i), encoded[i].length, zstdBytes,
                    schemaBytes, schemaFileBytes, batchBytes, batchZstd, batchHash, batchContentHash));
        }
        return rows;
    }

    /**
     * Тот же {@link #toJson}, но ключи объекта — в АЛФАВИТНОМ порядке
     * (через {@link TreeMap}), а не в порядке декларации записи. Нужен
     * только для batch_content_hash: канонический вид, не зависящий от
     * того, в каком порядке конкретная библиотека решила расположить
     * ключи при сериализации/десериализации (см. sizeRows выше).
     */
    private static ObjectNode toCanonicalJson(Map<String, Value> record) {
        return toJson(new TreeMap<>(record));
    }

    private static String sha256Hex(byte[] data) {
        try {
            byte[] digest = java.security.MessageDigest.getInstance("SHA-256").digest(data);
            StringBuilder sb = new StringBuilder(digest.length * 2);
            for (byte b : digest) sb.append(String.format("%02x", b));
            return sb.toString();
        } catch (java.security.NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    /**
     * Вес КАНОНИЧЕСКОЙ формы схемы — решение по плечу (круг ревью 2,
     * находка C2):
     *
     * <ul>
     * <li>avro — Parsing Canonical Form из спецификации Avro, её же
     *     считает {@link org.apache.avro.SchemaNormalization#toParsingForm};
     *     это официальная форма формата, а не решение стенда;</li>
     * <li>protobuf — дескриптор УЖЕ канонический: он собирается БЕЗ
     *     source_code_info (--exclude-source-info, круг ревью 2,
     *     находка C1), другой необязательной для чтения информации
     *     Protobuf в дескриптор не кладёт;</li>
     * <li>json-schema — минифицированный вид без декоративного title;
     *     канонической формы у JSON Schema нет, это решение стенда.</li>
     * </ul>
     */
    private static int canonicalWeight(String format, SchemaModel writer, Stand.SchemaFile writerFile) {
        return switch (format) {
            case "avro" -> org.apache.avro.SchemaNormalization.toParsingForm(writer.avro())
                    .getBytes(StandardCharsets.UTF_8).length;
            case "protobuf" -> writerFile.bytes().length;
            case "json-schema" -> canonicalJsonSchema(writerFile.bytes()).length;
            default -> throw new IllegalArgumentException("канонический вес не определён для плеча " + format);
        };
    }

    /**
     * Переводит документ JSON Schema в минифицированный вид: тот же
     * порядок пар ключ-значение, что в исходном файле (Jackson
     * {@link ObjectNode} — LinkedHashMap внутри, порядок вставки при
     * разборе текста совпадает с порядком в источнике — то же самое,
     * что обязана делать Go-часть при межъязыковой сверке), без отступов
     * и без верхнеуровневого "title". "$schema" остаётся — без него
     * документ неинтерпретируем.
     */
    private static byte[] canonicalJsonSchema(byte[] raw) {
        JsonNode root;
        try {
            root = MAPPER.readTree(raw);
        } catch (Exception e) {
            throw new Failures.SchemaSetup("JSON Schema не разобрана для канонической формы", e);
        }
        if (!(root instanceof ObjectNode obj)) {
            throw new Failures.SchemaSetup("верхний уровень JSON Schema — не объект");
        }
        // title декоративен: человекочитаемое имя схемы, не влияющее на
        // то, что она проверяет.
        obj.remove("title");
        try {
            return MAPPER.writeValueAsBytes(obj);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }

    /**
     * Обрамление пачки: 4 байта длины (uint32, big-endian — то же, что
     * {@link java.io.DataOutputStream#writeInt}) + сами байты записи,
     * подряд для всех записей клетки, без разделителей. Самодельная
     * рамка стенда, единая для всех четырёх плеч и ОБЯЗАННАЯ совпасть
     * побайтово с Go-частью (см. frameBatch в cmd/probe/main.go и
     * spec.md §10.3.2).
     */
    private static byte[] frameBatch(byte[][] records) {
        java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream();
        try (java.io.DataOutputStream data = new java.io.DataOutputStream(out)) {
            for (byte[] rec : records) {
                data.writeInt(rec.length);
                data.write(rec);
            }
        } catch (java.io.IOException e) {
            // ByteArrayOutputStream не бросает IOException по-настоящему —
            // обёртка нужна только из-за сигнатуры DataOutputStream.
            throw new IllegalStateException(e);
        }
        return out.toByteArray();
    }

    private static ObjectNode sizeRow(Args a, int index, Map<String, Value> record,
                                       int bytes, int zstdBytes, int schemaBytes, int schemaFileBytes,
                                       int batchBytes, int batchZstd, String batchHash, String batchContentHash) {
        ObjectNode node = MAPPER.createObjectNode();
        node.put("cell", String.join("/", LANG, a.op, a.format,
                a.change.isEmpty() ? "-" : a.change, a.direction, Integer.toString(index)));
        node.put("kind", a.op);
        node.put("format", a.format);
        node.put("change", a.change);
        node.put("direction", a.direction);
        node.put("record_index", index);
        node.put("lang", LANG);
        node.put("bytes", bytes);
        node.put("zstd", zstdBytes);
        // Без omitempty-подобной логики: у контроля schema_bytes РОВНО
        // ноль, и это содержательный ноль, а не пропущенное значение.
        // schema_bytes — КАНОНИЧЕСКАЯ форма (основная величина, круг
        // ревью 2, находка C2); schema_file_bytes — вес файла как есть,
        // для сравнения во что обходится наше форматирование исходника.
        node.put("schema_bytes", schemaBytes);
        node.put("schema_file_bytes", schemaFileBytes);
        node.put("batch_bytes", batchBytes);
        node.put("batch_zstd", batchZstd);
        node.put("batch_hash", batchHash);
        node.put("batch_content_hash", batchContentHash);
        node.set("record", toJson(record));
        return node;
    }

    // ------------------------------------------------------- виды пробы

    private static ObjectNode compat(Args a, int index, Map<String, Value> record,
                                     SchemaModel writer, SchemaModel reader, Codec codec) {
        Map<String, Value> want = Expect.compute(writer, reader, record);

        String stage = "encode";
        byte[] bytes;
        try {
            bytes = codec.encode(record);
        } catch (Failures.FormatRefusal e) {
            return row(a, index, stage, "refused", false, 0, record, want, null, e.getMessage());
        }

        stage = "decode";
        Map<String, Value> got;
        try {
            got = codec.decode(bytes);
        } catch (Failures.FormatRefusal e) {
            return row(a, index, stage, "refused", true, bytes.length, record, want, null, e.getMessage());
        }

        // got передаётся в строку (Задача 6): у исхода wrong это
        // единственное поле, подтверждающее «прочиталось, но не то»
        // наблюдаемым значением, а не словом классификатора.
        return row(a, index, stage, classify(got, want), true, bytes.length, record, want, got, null);
    }

    /**
     * Классификация исхода — ЕДИНАЯ функция для обычной и перекрёстной
     * пробы (Задача 8, решение контроллера): приём чужих байтов
     * (crossAcceptRows) обязан вызывать ТУ ЖЕ функцию, что и обычная
     * проба (compat), иначе перекрёстная колонка мерила бы не то же
     * самое, что колонка эволюции. Отказ формата (Failures.FormatRefusal)
     * классифицируется на месте вызова, ДО этой функции: она решает
     * только между "ok" и "wrong" — тем, что физически можно узнать,
     * только сравнив прочитанное с ожиданием.
     */
    private static String classify(Map<String, Value> got, Map<String, Value> want) {
        return got.equals(want) ? "ok" : "wrong";
    }

    private static ObjectNode roundtrip(Args a, int index, Map<String, Value> record,
                                        SchemaModel writer, SchemaModel reader, Codec codec) {
        // Ожидание для итоговой сверки — то, которое даёт пара «схема
        // писателя против неё же самой»: последнее чтение идёт ТОЙ ЖЕ
        // схемой, что и первая запись.
        Map<String, Value> want = Expect.compute(writer, writer, record);
        Map<String, Value> cross = Expect.compute(writer, reader, record);

        // Причина n/a № 3: пара схем не сводится к чистому отбрасыванию
        // полей. Иначе читатель не «не знает» поле, а видит его ИНАЧЕ, и
        // проба измеряла бы не заявленное свойство, а побочный эффект.
        // Проверка чисто структурная и названия изменения не смотрит.
        if (!isProperDroppingSubset(cross, want)) {
            return naRow(a, index, "круговая проба: пара схем не сводится к чистому "
                    + "отбрасыванию полей — читатель видит поля иначе, а не просто их не знает");
        }

        String stage = "encode";
        byte[] first;
        try {
            first = codec.encode(record);
        } catch (Failures.FormatRefusal e) {
            return row(a, index, stage, "refused", false, 0, record, want, null, e.getMessage());
        }

        // Размер в строке — от ПЕРВОГО кодирования: второе сравнивать не
        // с чем, а два числа под одним именем сделали бы столбец
        // бессмысленным.
        int size = first.length;

        stage = "decode";
        Object state;
        try {
            state = codec.decodeKeepingRemainder(first);
        } catch (Failures.FormatRefusal e) {
            return row(a, index, stage, "refused", true, size, record, want, null, e.getMessage());
        }

        stage = "encode-state";
        byte[] again;
        try {
            again = codec.encodeState(state);
        } catch (Failures.FormatRefusal e) {
            return row(a, index, stage, "refused", true, size, record, want, null, e.getMessage());
        }

        stage = "decode-final";
        Map<String, Value> got;
        try {
            got = codec.decodeWithWriter(again);
        } catch (Failures.FormatRefusal e) {
            return row(a, index, stage, "refused", true, size, record, want, null, e.getMessage());
        }

        return row(a, index, stage, got.equals(want) ? "ok" : "wrong", true, size, record, want, got, null);
    }

    /**
     * Набор имён «писатель против читателя» обязан быть СОБСТВЕННЫМ
     * подмножеством набора «писатель против писателя», и каждое значение
     * — совпадать со своим.
     */
    private static boolean isProperDroppingSubset(Map<String, Value> cross, Map<String, Value> self) {
        if (cross.size() >= self.size()) return false;
        for (Map.Entry<String, Value> e : cross.entrySet()) {
            if (!self.containsKey(e.getKey())) return false;
            if (!self.get(e.getKey()).equals(e.getValue())) return false;
        }
        return true;
    }

    // ------------------------------------------ Задача 8: перекрёстное чтение

    /**
     * «Отдать байты» (spec.md §17.2): кодирует каждую каноническую
     * запись клетки схемой ПИСАТЕЛЯ и кладёт результат в файл обмена.
     * Ничего не печатает — этот вид пробы не производит строку
     * результата, только артефакт для чтения другим процессом (в другом
     * вызове, возможно другой реализацией).
     *
     * Отказ кодирования — сбой СТЕНДА, не находка про формат:
     * канонические записи по построению проходят §8.1 (уже проверено
     * выше, до вызова), и отказ означал бы, что сломалась проба (тот же
     * принцип, что и у sizeRows).
     */
    private static List<ObjectNode> crossEmitRows(Args a, List<Map<String, Value>> records,
                                                   Codec codec, Path exchangeDir) {
        for (int i = 0; i < records.size(); i++) {
            byte[] bytes;
            try {
                bytes = codec.encode(records.get(i));
            } catch (Failures.FormatRefusal e) {
                throw new Failures.ProbeRefusal("--op=cross-emit: " + a.format
                        + " отказался закодировать каноническую запись " + i + ": " + e.getMessage());
            }
            Exchange.writeCross(exchangeDir, LANG, a.format, a.change, a.direction, i, bytes);
        }
        return List.of();
    }

    /**
     * «Принять чужие байты» (spec.md §17.2): читает файл обмена,
     * записанный языком {@code a.writerLang} на ЭТИХ ЖЕ координатах, и
     * классифицирует исход функцией {@link #classify}, которой пользуется
     * и compat() — требование решения контроллера Задачи 8, без которого
     * перекрёстная колонка мерила бы не то же самое, что колонка
     * эволюции.
     *
     * Целостность файла обмена (Exchange.readCross: координаты и
     * дайджест) проверяется ДО декодирования её неудача бросает
     * Failures.ProbeRefusal и останавливает вывод целиком (симметрично
     * §12 spec.md) — файл обмена испорчен или подменён, это наша
     * поломка, а не поведение формата.
     */
    private static List<ObjectNode> crossAcceptRows(Args a, List<Map<String, Value>> records,
                                                      SchemaModel writer, SchemaModel reader, Codec codec,
                                                      Path exchangeDir) {
        List<ObjectNode> rows = new ArrayList<>();
        for (int i = 0; i < records.size(); i++) {
            Map<String, Value> record = records.get(i);
            // Ожидание считается ИЗ ЛОКАЛЬНОЙ канонической записи и
            // локально прочитанных схем — записи в файле обмена не
            // передаются, только байты: обе реализации уже разделяют
            // один и тот же schemas/records.json (§3.4).
            Map<String, Value> want = Expect.compute(writer, reader, record);

            byte[] bytes = Exchange.readCross(exchangeDir, a.writerLang, a.format, a.change, a.direction, i);

            Map<String, Value> got;
            try {
                got = codec.decode(bytes);
            } catch (Failures.FormatRefusal e) {
                rows.add(crossRow(a, i, bytes.length, record, want, null, "refused", e.getMessage()));
                continue;
            }
            rows.add(crossRow(a, i, bytes.length, record, want, got, classify(got, want), null));
        }
        return rows;
    }

    /**
     * Контроль байтовой идентичности (spec.md §17.6): кодирует
     * каноническую запись №0 схемой писателя ДВАЖДЫ, в этом же процессе,
     * и сравнивает результат. Только эта запись выбрана намеренно и одна
     * — вопрос контроля «детерминирован ли кодек вообще», а не «на каких
     * записях»: тот же кодек детерминирован (или нет) для любой записи
     * одной схемы.
     *
     * НЕ решает, совпадают ли байты МЕЖДУ Go и Java — это делает разбор
     * (scripts/analyze-cross.py) сравнением дайджестов двух таких строк,
     * снятых у разных языков; физическая передача байт для ЭТОГО вопроса
     * не нужна, довольно дайджеста.
     */
    private static List<ObjectNode> identityRows(Args a, List<Map<String, Value>> records, Codec codec) {
        if (records.isEmpty()) {
            throw new Failures.ProbeRefusal("--op=identity: нет ни одной канонической записи");
        }
        Map<String, Value> record = records.get(0);
        byte[] b1;
        byte[] b2;
        try {
            b1 = codec.encode(record);
            b2 = codec.encode(record);
        } catch (Failures.FormatRefusal e) {
            throw new Failures.ProbeRefusal("--op=identity: " + a.format
                    + " отказался закодировать каноническую запись 0: " + e.getMessage());
        }
        ObjectNode node = MAPPER.createObjectNode();
        node.put("kind", "identity-probe");
        node.put("format", a.format);
        node.put("change", a.change);
        node.put("lang", LANG);
        node.put("control_equal", java.util.Arrays.equals(b1, b2));
        node.put("sha256", sha256Hex(b1));
        node.put("bytes", b1.length);
        return List.of(node);
    }

    private static ObjectNode crossRow(Args a, int index, int bytesLen,
                                        Map<String, Value> record, Map<String, Value> want,
                                        Map<String, Value> got, String outcome, String error) {
        ObjectNode node = MAPPER.createObjectNode();
        node.put("cell", String.join("/", LANG, a.op, a.format,
                a.change.isEmpty() ? "-" : a.change, a.direction, Integer.toString(index)));
        // kind="cross", а не a.op ("cross-accept"): это форма строки
        // МЕЖЪЯЗЫКОВОЙ пробы, симметричная одноимённому виду в Go-части
        // и в интерфейсе Задачи 8 — сборщик отличает её от compat/roundtrip
        // по этому полю, а не по op в имени cell.
        node.put("kind", "cross");
        node.put("format", a.format);
        node.put("change", a.change);
        node.put("direction", a.direction);
        node.put("record_index", index);
        node.put("stage", "decode");
        node.put("lang", LANG);
        node.put("outcome", outcome);
        node.put("encoded", true);
        node.put("bytes", bytesLen);
        node.set("record", toJson(record));
        node.set("want", toJson(want));
        // got — Задача 6/8: ФАКТИЧЕСКИ прочитанная запись, только когда
        // декодирование состоялось; у "refused" его нет — там наблюдать
        // нечего.
        if (got != null) node.set("got", toJson(got));
        if (error != null) node.put("error", error);
        node.put("writer", a.writerLang);
        node.put("reader", LANG);
        return node;
    }

    // ------------------------------------------------------ строки вывода

    private static List<ObjectNode> notApplicable(Args a, int count, String why) {
        List<ObjectNode> rows = new ArrayList<>();
        // Строка печатается на каждую каноническую запись: клетка
        // описывает их все, и таблица остаётся однородной.
        for (int i = 0; i < count; i++) rows.add(naRow(a, i, why));
        return rows;
    }

    private static ObjectNode naRow(Args a, int index, String why) {
        return row(a, index, "", "n/a", false, 0, null, null, null, why);
    }

    private static List<ObjectNode> errorRows(Args a, int count, String why) {
        List<ObjectNode> rows = new ArrayList<>();
        for (int i = 0; i < count; i++) {
            // Схема не заработала — кодирование не запускалось, значит
            // стадии нет; ожидания тоже нет, его не из чего считать.
            rows.add(row(a, i, "", "error", false, 0, null, null, null, why));
        }
        return rows;
    }

    private static ObjectNode row(Args a, int index, String stage, String outcome,
                                  boolean encoded, int bytes,
                                  Map<String, Value> record, Map<String, Value> want,
                                  Map<String, Value> got, String error) {
        ObjectNode node = MAPPER.createObjectNode();
        node.put("cell", String.join("/", LANG, a.op, a.format,
                a.change.isEmpty() ? "-" : a.change, a.direction, Integer.toString(index)));
        node.put("kind", a.op);
        node.put("format", a.format);
        node.put("change", a.change);
        node.put("direction", a.direction);
        node.put("record_index", index);
        node.put("stage", stage);
        node.put("lang", LANG);
        node.put("outcome", outcome);
        // Без этого признака строки n/a завышают счёт фактических
        // кодирований ровно вдвое: нулевой размер бывает и у отказа на
        // кодировании.
        node.put("encoded", encoded);
        node.put("bytes", bytes);
        if (record != null) node.set("record", toJson(record));
        if (want != null) node.set("want", toJson(want));
        // got — Задача 6: ФАКТИЧЕСКИ прочитанная запись, заполняется
        // только когда декодирование реально состоялось (успешно) —
        // отсутствует у refused/error/n/a, там наблюдать нечего.
        if (got != null) node.set("got", toJson(got));
        if (error != null) node.put("error", error);
        return node;
    }

    private static ObjectNode toJson(Map<String, Value> record) {
        ObjectNode node = MAPPER.createObjectNode();
        for (Map.Entry<String, Value> e : record.entrySet()) {
            Value v = e.getValue();
            switch (v.cat()) {
                case INT -> node.put(e.getKey(), v.asLong());
                case STRING -> node.put(e.getKey(), v.asString());
                case BOOL -> node.put(e.getKey(), (Boolean) v.raw());
                case NULL -> node.putNull(e.getKey());
                // Значение, категорию которого стенд назвать не смог, до
                // печати не доходит: запись с ним отвергается раньше.
                // Печатается как есть — превращать его в строку значило бы
                // подменить в отчёте то, что наблюдали.
                case UNKNOWN -> node.set(e.getKey(), MAPPER.valueToTree(v.raw()));
                // RECORD — задача 6bis (retype_message): вложенное
                // сообщение печатается РЕКУРСИВНО той же функцией, а не
                // как сырой Java-объект — иначе строка результата не
                // была бы валидным JSON и не совпадала бы по форме с Go.
                case RECORD -> node.set(e.getKey(), toJson(v.asRecord()));
            }
        }
        return node;
    }

    // ---------------------------------------------------------- аргументы

    private static final class Args {
        final String format;
        final String change;
        final String direction;
        final String op;
        // writerLang — только для --op=cross-accept: язык, чьи байты
        // принимаются через файл обмена. Не координата §4.1 (не входит в
        // перечень аргументов клетки) — это четвёртая координата ИМЕННО
        // оси перекрёстного чтения, «кто записал», симметричная тому, что
        // вызывающий язык (LANG) всегда и есть «кто читает» (spec.md
        // §17.2). У остальных видов пробы значение всегда null.
        final String writerLang;

        private Args(String format, String change, String direction, String op, String writerLang) {
            this.format = format;
            this.change = change;
            this.direction = direction;
            this.op = op;
            this.writerLang = writerLang;
        }

        static Args parse(String[] argv) {
            Map<String, String> named = new LinkedHashMap<>();
            for (String arg : argv) {
                if (!arg.startsWith("--") || arg.indexOf('=') < 0) {
                    throw new Failures.ProbeRefusal("аргумент не в форме --имя=значение: " + arg);
                }
                int eq = arg.indexOf('=');
                String name = arg.substring(2, eq);
                if (named.put(name, arg.substring(eq + 1)) != null) {
                    throw new Failures.ProbeRefusal("аргумент повторён: --" + name);
                }
            }
            for (String name : named.keySet()) {
                if (!Set.of("format", "change", "direction", "op", "writer-lang").contains(name)) {
                    throw new Failures.ProbeRefusal("аргумента --" + name + " у пробы нет");
                }
            }

            String format = require(named, "format", FORMATS);
            String change = require(named, "change", CHANGES);
            String direction = require(named, "direction", DIRECTIONS);
            String op = named.getOrDefault("op", "compat");
            if (!OPS.contains(op)) {
                throw new Failures.ProbeRefusal("вид пробы вне перечня: " + op);
            }
            if (change.equals("base") && !direction.equals("same")) {
                throw new Failures.ProbeRefusal(
                        "у базовой версии нет второй половины пары: base несовместимо с " + direction);
            }
            // Размер и идентичность — свойства ОДНОЙ схемы, а не пары
            // «писатель/читатель»: newer_reader и newer_writer выбирают
            // ДВЕ разные схемы, и вопрос «чья схема» не имел бы
            // однозначного ответа.
            if ((op.equals("size") || op.equals("identity")) && !direction.equals("same")) {
                throw new Failures.ProbeRefusal(
                        "--op=" + op + ": направление обязано быть same — измеряется одна схема, а не пара писатель/читатель");
            }
            String writerLang = named.get("writer-lang");
            if (op.equals("cross-accept")) {
                if (writerLang == null || !WRITER_LANGS.contains(writerLang)) {
                    throw new Failures.ProbeRefusal(
                            "--op=cross-accept: --writer-lang обязателен и допускает только go или java, получено "
                                    + writerLang);
                }
            } else if (writerLang != null) {
                throw new Failures.ProbeRefusal("--writer-lang имеет смысл только при --op=cross-accept");
            }
            return new Args(format, change, direction, op, writerLang);
        }

        private static String require(Map<String, String> named, String name, Set<String> allowed) {
            String v = named.get(name);
            if (v == null) throw new Failures.ProbeRefusal("не задан обязательный аргумент --" + name);
            if (!allowed.contains(v)) {
                throw new Failures.ProbeRefusal("значение --" + name + " вне перечня: " + v);
            }
            return v;
        }
    }
}
