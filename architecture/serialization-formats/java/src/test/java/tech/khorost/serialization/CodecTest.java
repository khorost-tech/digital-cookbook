package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.io.ByteArrayOutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Проверяется то, что проверяемо здесь и сейчас, без чужих фикстур.
 *
 * Межъязыковое сравнение байтов (Go против Java) сюда НЕ входит: у него
 * нет контрольного плеча, а без контроля такое сравнение недействительно.
 * Оно живёт отдельной задачей.
 */
class CodecTest {

    private static final ObjectMapper M = new ObjectMapper();

    /**
     * Круговой прогон по каждому плечу: закодировать эталонную запись
     * схемой, декодировать ТОЙ ЖЕ схемой, сверить с исходной.
     *
     * Это минимальное свойство кодека: пока оно не выполняется, любые
     * выводы про эволюцию схем — про поломку пробы, а не про формат.
     */
    @Test
    void everyArmSurvivesEncodeDecodeWithOneSchema() throws Exception {
        Stand stand = Stand.locate();
        List<Map<String, Value>> records = stand.records(1, "");

        for (String format : List.of("json", "json-schema", "avro", "protobuf")) {
            Stand.SchemaFile file = stand.schema(Stand.notationOf(format), 1, "");
            SchemaModel model = SchemaModel.parse(file);

            Map<String, Value> record = records.get(0);
            Codec codec = Codec.of(format, model, model);

            byte[] bytes = codec.encode(record);
            assertTrue(bytes.length > 0, format + ": кодирование дало пустые байты");

            Map<String, Value> back = codec.decode(bytes);
            assertEquals(record, back, format + ": круговой прогон одной схемой изменил запись");
        }
    }

    /**
     * Строка-исход обязана быть валидным JSON и нести все поля протокола.
     * Поле lang — литерал, по которому делятся потоки результатов двух
     * реализаций; ошибка в нём сливает их в один.
     */
    @Test
    void outcomeLineCarriesTheWholeProtocol() throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        ByteArrayOutputStream err = new ByteArrayOutputStream();
        int code = Probe.run(
                new String[]{"--format=avro", "--change=rename", "--direction=newer_reader", "--op=compat"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(err, true, StandardCharsets.UTF_8));

        assertEquals(0, code, "проба вернула не 0: " + err.toString(StandardCharsets.UTF_8));

        String[] lines = out.toString(StandardCharsets.UTF_8).strip().split("\\R");
        assertEquals(5, lines.length, "по строке на каждую каноническую запись");

        for (int i = 0; i < lines.length; i++) {
            JsonNode row = M.readTree(lines[i]);
            for (String field : List.of("cell", "kind", "format", "change", "direction",
                    "record_index", "stage", "lang", "outcome", "encoded", "bytes")) {
                assertTrue(row.has(field), "нет поля " + field + " в строке: " + lines[i]);
            }
            assertEquals("java", row.get("lang").asText(), "литерал реализации");
            assertEquals("compat", row.get("kind").asText());
            assertEquals("avro", row.get("format").asText());
            assertEquals(i, row.get("record_index").asInt());
            assertEquals("java/compat/avro/rename/newer_reader/" + i, row.get("cell").asText(),
                    "имя строки обязано быть самодостаточным");
        }
    }

    /**
     * Равенство определено по КАТЕГОРИИ и значению, а не по типам языка.
     * В Java Integer(1).equals(Long(1)) — ложь; если бы равенство было
     * унаследовано от языка, колонка развалилась бы молча.
     */
    @Test
    void integerWidthDoesNotAffectEquality() {
        assertEquals(Value.of(1), Value.of(1L), "целые разной ширины — одно значение");
        assertNotEquals(Value.of(1L), Value.of("1"), "целое и строка — разные категории");
        assertNotEquals(Value.NULL, Value.of(0L), "«пусто» и нулевое значение — разные категории");
        assertNotEquals(Value.NULL, Value.of(""), "«пусто» и пустая строка — разные категории");
    }

    /**
     * Отсутствие поля и поле со значением «пусто» — не одно и то же.
     */
    @Test
    void absentFieldDiffersFromNullField() {
        Map<String, Value> withNull = new LinkedHashMap<>();
        withNull.put("age", Value.NULL);
        assertNotEquals(new LinkedHashMap<String, Value>(), withNull);
    }

    /**
     * Разбор строки в целое — строгий: спека перечисляет ровно знак и
     * цифры. Неразбираемая строка остаётся НЕИЗМЕНЁННОЙ, иначе честный
     * wrong превратился бы в случайный ok.
     */
    @Test
    void stringToIntCastIsStrictAndNonDestructive() {
        assertEquals(Value.of(12L), Expect.cast(Value.of("12"), Value.Cat.INT));
        assertEquals(Value.of(-12L), Expect.cast(Value.of("-12"), Value.Cat.INT));
        assertEquals(Value.of(12L), Expect.cast(Value.of("+12"), Value.Cat.INT));
        for (String bad : List.of("", " 12", "12 ", "1_2", "0x10", "12.0", "1e3", "99999999999999999999")) {
            assertEquals(Value.of(bad), Expect.cast(Value.of(bad), Value.Cat.INT),
                    "неразбираемая строка обязана остаться собой: «" + bad + "»");
        }
        assertEquals(Value.of("12"), Expect.cast(Value.of(12L), Value.Cat.STRING));
    }

    /**
     * Строки Avro приходят как org.apache.avro.util.Utf8, и
     * utf8.equals("Анна") даёт false. Без приведения две трети клеток
     * Avro объявились бы неверным чтением.
     */
    @Test
    void avroUtf8NormalizesToString() {
        assertEquals(Value.of("Анна"), Value.of(new org.apache.avro.util.Utf8("Анна")));
    }

    // ----------------------------------------------------- --op=size (Задача 5)

    /**
     * Строка вида size несёт своё собственное подмножество полей —
     * bytes, zstd, schema_bytes — а не форму строки compat/roundtrip
     * (у оси размера нет ни outcome, ни stage: это не сравнение
     * писателя с читателем, а измерение одной схемы).
     */
    @Test
    void sizeRowCarriesBytesZstdAndSchemaBytes() throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        ByteArrayOutputStream err = new ByteArrayOutputStream();
        int code = Probe.run(
                new String[]{"--format=avro", "--change=base", "--direction=same", "--op=size"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(err, true, StandardCharsets.UTF_8));
        assertEquals(0, code, "проба вернула не 0: " + err.toString(StandardCharsets.UTF_8));

        String[] lines = out.toString(StandardCharsets.UTF_8).strip().split("\\R");
        assertEquals(5, lines.length, "по строке на каждую каноническую запись");
        for (String line : lines) {
            JsonNode row = M.readTree(line);
            for (String field : List.of("cell", "kind", "format", "lang", "bytes", "zstd",
                    "schema_bytes", "schema_file_bytes", "batch_bytes", "batch_zstd", "batch_hash")) {
                assertTrue(row.has(field), "нет поля " + field + " в строке: " + line);
            }
            assertEquals("size", row.get("kind").asText());
            assertEquals("java", row.get("lang").asText());
            assertTrue(row.get("bytes").asInt() > 0, "bytes обязан быть больше нуля: " + line);
            assertTrue(row.get("schema_bytes").asInt() > 0, "у avro schema_bytes обязан быть больше нуля: " + line);
            // Круг ревью 2, находка C2: каноническая форма (Parsing
            // Canonical Form у Avro) обязана быть строго легче файла,
            // размеченного нашими отступами.
            assertTrue(row.get("schema_bytes").asInt() < row.get("schema_file_bytes").asInt(),
                    "у avro schema_bytes обязан быть меньше schema_file_bytes: " + line);
        }
    }

    /**
     * Круг ревью 2, находка C2: канонический вес Avro — Parsing
     * Canonical Form из спецификации формата, число известное и не
     * должно тихо съехать при смене версии библиотеки. Протобуф —
     * дескриптор уже канонический (без source_code_info, находка C1),
     * поэтому его schema_bytes == schema_file_bytes.
     */
    @Test
    void canonicalSchemaWeightMatchesKnownValues() throws Exception {
        assertEquals(162, firstSizeRow("avro").get("schema_bytes").asInt());
        assertEquals(221, firstSizeRow("avro").get("schema_file_bytes").asInt());
        assertEquals(119, firstSizeRow("protobuf").get("schema_bytes").asInt());
        assertEquals(119, firstSizeRow("protobuf").get("schema_file_bytes").asInt(),
                "у protobuf канонический вес и вес файла обязаны совпасть — дескриптор уже канонический");
    }

    private static JsonNode firstSizeRow(String format) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        int code = Probe.run(new String[]{"--format=" + format, "--change=base", "--direction=same", "--op=size"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(new ByteArrayOutputStream(), true, StandardCharsets.UTF_8));
        assertEquals(0, code, format);
        return M.readTree(out.toString(StandardCharsets.UTF_8).strip().split("\\R")[0]);
    }

    /**
     * Круг ревью 2, находки C3+C4: закоммиченное число сжатой пачки
     * protobuf у Go не воспроизводилось (порядок обхода полей у
     * *dynamicpb.Message был недетерминирован). Java использует
     * DynamicMessage, а не dynamicpb — сериализация полей у него другая
     * реализация, но проверить стоит ровно то же самое: содержимое
     * пачки (не длину — длина не изменилась бы даже при порче,
     * находка M1), а не сжатую ОДНУ запись, которой расходиться нечем.
     */
    @Test
    void protobufBatchHashIsDeterministicAcrossEightRuns() throws Exception {
        String firstHash = null;
        int firstZstd = -1;
        for (int i = 0; i < 8; i++) {
            JsonNode row = firstSizeRow("protobuf");
            String hash = row.get("batch_hash").asText();
            int zstd = row.get("batch_zstd").asInt();
            if (firstHash == null) {
                firstHash = hash;
                firstZstd = zstd;
            } else {
                assertEquals(firstHash, hash, "прогон " + i + ": batch_hash разошёлся");
                assertEquals(firstZstd, zstd, "прогон " + i + ": batch_zstd разошёлся");
            }
        }
    }

    /**
     * Круг ревью 3: batch_content_hash хеширует СОДЕРЖИМОЕ после
     * расшифровки (ключи объекта — по алфавиту), а не байты с провода.
     * Оно обязано совпасть у ЛЮБЫХ форматов на одних и тех же
     * канонических данных — формат кодирования на него не влияет,
     * в отличие от batch_hash, у которого разные байты на проводе для
     * разных форматов по построению.
     */
    @Test
    void batchContentHashIsTheSameAcrossAllFourFormats() throws Exception {
        String jsonHash = firstSizeRow("json").get("batch_content_hash").asText();
        for (String format : List.of("json-schema", "avro", "protobuf")) {
            String hash = firstSizeRow(format).get("batch_content_hash").asText();
            assertEquals(jsonHash, hash,
                    format + ": batch_content_hash разошёлся с json — расшифрованное содержимое "
                            + "обязано совпасть независимо от формата");
        }
    }

    /**
     * У json/json-schema нет канонической формы байт (JSON не
     * специфицирует порядок ключей объекта) — batch_hash там ожидаемо
     * отличается от того, что дала бы другая реализация, а
     * batch_content_hash остаётся единственным полем, доказывающим
     * межреализационное равенство. Тест документирует, что оба поля
     * присутствуют и различны на этом плече.
     */
    @Test
    void batchHashAndContentHashAreDifferentFieldsForJson() throws Exception {
        JsonNode row = firstSizeRow("json");
        String batchHash = row.get("batch_hash").asText();
        String batchContentHash = row.get("batch_content_hash").asText();
        assertFalse(batchHash.isEmpty());
        assertFalse(batchContentHash.isEmpty());
        assertNotEquals(batchHash, batchContentHash,
                "batch_hash и batch_content_hash случайно совпали — тест ничего не показывает");
    }

    /**
     * Контрольное плечо и json-schema обязаны дать ПОБАЙТОВО РАВНЫЙ
     * bytes (заявление будущей статьи, и оно проверяется, а не
     * предполагается), а вес схемы у контроля обязан быть нулём —
     * содержательным, а не пропущенным.
     */
    @Test
    void sizeControlArmMatchesJsonSchemaAndHasNoSchemaWeight() throws Exception {
        int[] controlBytes = sizeBytesPerRecord("json");
        int[] schemaBytes = sizeBytesPerRecord("json-schema");
        assertArrayEquals(controlBytes, schemaBytes, "контроль обязан совпасть побайтово с json-schema");

        ByteArrayOutputStream out = new ByteArrayOutputStream();
        Probe.run(new String[]{"--format=json", "--change=base", "--direction=same", "--op=size"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(new ByteArrayOutputStream(), true, StandardCharsets.UTF_8));
        for (String line : out.toString(StandardCharsets.UTF_8).strip().split("\\R")) {
            JsonNode row = M.readTree(line);
            assertEquals(0, row.get("schema_bytes").asInt(),
                    "у контроля schema_bytes обязан быть 0 — читателю схема не нужна вовсе: " + line);
            assertEquals(0, row.get("schema_file_bytes").asInt(),
                    "у контроля schema_file_bytes обязан быть 0 тоже: " + line);
        }
    }

    private static int[] sizeBytesPerRecord(String format) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        int code = Probe.run(new String[]{"--format=" + format, "--change=base", "--direction=same", "--op=size"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(new ByteArrayOutputStream(), true, StandardCharsets.UTF_8));
        assertEquals(0, code, format);
        String[] lines = out.toString(StandardCharsets.UTF_8).strip().split("\\R");
        int[] result = new int[lines.length];
        for (int i = 0; i < lines.length; i++) {
            result[i] = M.readTree(lines[i]).get("bytes").asInt();
        }
        return result;
    }

    /**
     * Размер — свойство ОДНОЙ схемы: newer_reader/newer_writer выбирают
     * ДВЕ разные схемы, и вопрос «чей размер» не имел бы однозначного
     * ответа. --op=size с таким направлением обязан быть отказом пробы.
     */
    @Test
    void sizeRequiresSameDirection() {
        for (String direction : List.of("newer_reader", "newer_writer")) {
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            ByteArrayOutputStream err = new ByteArrayOutputStream();
            int code = Probe.run(
                    new String[]{"--format=avro", "--change=add_default", "--direction=" + direction, "--op=size"},
                    new PrintStream(out, true, StandardCharsets.UTF_8),
                    new PrintStream(err, true, StandardCharsets.UTF_8));
            assertEquals(1, code, "направление " + direction + " обязано быть отказом");
            assertEquals(0, out.size(), "вывода при отказе быть не должно");
        }
    }

    /**
     * batch_bytes/batch_zstd — свойства КЛЕТКИ (spec.md §10.3.2), а не
     * отдельной записи: одно и то же значение обязано быть на всех пяти
     * строках, и оно обязано быть больше нуля.
     */
    @Test
    void batchSizeIsSameAcrossAllRecordsOfTheCell() throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        int code = Probe.run(new String[]{"--format=avro", "--change=base", "--direction=same", "--op=size"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(new ByteArrayOutputStream(), true, StandardCharsets.UTF_8));
        assertEquals(0, code);

        String[] lines = out.toString(StandardCharsets.UTF_8).strip().split("\\R");
        assertEquals(5, lines.length);
        int batchBytes = M.readTree(lines[0]).get("batch_bytes").asInt();
        int batchZstd = M.readTree(lines[0]).get("batch_zstd").asInt();
        assertTrue(batchBytes > 0, "batch_bytes обязан быть больше нуля");
        assertTrue(batchZstd > 0, "batch_zstd обязан быть больше нуля");
        for (String line : lines) {
            JsonNode row = M.readTree(line);
            assertEquals(batchBytes, row.get("batch_bytes").asInt(), "batch_bytes разошёлся внутри клетки: " + line);
            assertEquals(batchZstd, row.get("batch_zstd").asInt(), "batch_zstd разошёлся внутри клетки: " + line);
        }
    }

    /**
     * Обрамление добавляет ровно 4 байта на запись (uint32-длина) — не
     * теряется и не удваивается при склейке пяти записей в одну пачку.
     */
    @Test
    void batchBytesAccountsForFraming() throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        Probe.run(new String[]{"--format=protobuf", "--change=base", "--direction=same", "--op=size"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(new ByteArrayOutputStream(), true, StandardCharsets.UTF_8));
        String[] lines = out.toString(StandardCharsets.UTF_8).strip().split("\\R");
        int sumBytes = 0;
        for (String line : lines) {
            sumBytes += M.readTree(line).get("bytes").asInt();
        }
        int want = sumBytes + 4 * lines.length;
        assertEquals(want, M.readTree(lines[0]).get("batch_bytes").asInt(),
                "batch_bytes обязан быть суммой bytes плюс 4 байта обрамления на запись");
    }
}
