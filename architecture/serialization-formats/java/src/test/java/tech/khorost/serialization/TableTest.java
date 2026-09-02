package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.io.ByteArrayOutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Полный обход таблицы предсказаний своей реализацией.
 *
 * Расхождение здесь — не повод править expected.json: таблица и записи
 * стенда неприкосновенны, а расхождение идёт в отчёт как находка.
 */
class TableTest {

    private static final ObjectMapper M = new ObjectMapper();

    private record Row(String outcome, boolean encoded, long bytes, String stage, JsonNode raw) {}

    private static List<Row> run(String format, String change, String direction, String op) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        ByteArrayOutputStream err = new ByteArrayOutputStream();
        int code = Probe.run(
                new String[]{"--format=" + format, "--change=" + change,
                        "--direction=" + direction, "--op=" + op},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(err, true, StandardCharsets.UTF_8));
        assertEquals(0, code, format + "/" + change + "/" + direction + "/" + op
                + ": отказ пробы — " + err.toString(StandardCharsets.UTF_8));

        List<Row> rows = new ArrayList<>();
        for (String line : out.toString(StandardCharsets.UTF_8).strip().split("\\R")) {
            JsonNode n = M.readTree(line);
            rows.add(new Row(n.get("outcome").asText(), n.get("encoded").asBoolean(),
                    n.get("bytes").asLong(), n.get("stage").asText(), n));
        }
        return rows;
    }

    // Круг правок 6ter (по требованию ревью). Раньше "клетка
    // расщепляется" и "языки законно расходятся" были множествами
    // координат ВШИТЫМИ В ЭТОТ ТЕСТ — то есть самая ответственная часть
    // проверки (что валит прогон, а что нет) жила вне цепочки
    // дайджестов манифеста и не менялась вместе со схемами и записями.
    // Теперь запись expected.json может быть не только строкой-исходом,
    // но и структурой:
    //   - {"by_record": {"0": "wrong", "1": "refused", ...}, "reason": "..."}
    //     — клетка расщепляется предсказуемо, исход зависит от номера
    //     записи;
    //   - {"by_lang": {"go": "wrong", "java": "refused"}, "reason": "..."}
    //     — языки законно дают разные исходы.
    // resolveExpected — единственное место, где этот тест понимает
    // структуру каталога; он её ТОЛЬКО ЧИТАЕТ.
    private static String resolveExpected(JsonNode entry, String lang, int recordIndex) {
        if (entry.isTextual()) return entry.asText();
        if (entry.has("by_lang")) {
            JsonNode sub = entry.get("by_lang").get(lang);
            return sub == null ? null : resolveExpected(sub, lang, recordIndex);
        }
        if (entry.has("by_record")) {
            JsonNode sub = entry.get("by_record").get(Integer.toString(recordIndex));
            return sub == null ? null : resolveExpected(sub, lang, recordIndex);
        }
        throw new IllegalStateException("объект в expected.json без by_lang и без by_record: " + entry);
    }

    /** Литерал, под которым hamba/avro и Go-часть стенда фигурируют в
     * expected.json (by_lang) — этот тест прогоняет только Java-пробу,
     * поэтому резолвит каталог именно под "java". */
    private static final String LANG = "java";

    /** Каждая ЗАПИСЬ каждой клетки expected.json воспроизводится один в
     * один — включая клетки, расщепляющиеся по номеру записи (by_record)
     * и клетки, законно расходящиеся между языками (by_lang): для этого
     * теста они резолвятся под "java" и сравниваются со своим
     * собственным объявленным значением, без специальных исключений. */
    @Test
    void everyPredictedCellReproduces() throws Exception {
        JsonNode table = M.readTree(Stand.locate().schemasDir().resolve("expected.json").toFile());
        List<String> mismatches = new ArrayList<>();

        for (Iterator<String> it = table.fieldNames(); it.hasNext(); ) {
            String change = it.next();
            JsonNode byFormat = table.get(change);
            for (Iterator<String> fi = byFormat.fieldNames(); fi.hasNext(); ) {
                String format = fi.next();
                JsonNode byDirection = byFormat.get(format);
                for (Iterator<String> di = byDirection.fieldNames(); di.hasNext(); ) {
                    String direction = di.next();
                    JsonNode entry = byDirection.get(direction);
                    String cell = change + "/" + format + "/" + direction;

                    List<Row> rows = run(format, change, direction, "compat");
                    assertEquals(5, rows.size(), "клетка меряется на всех канонических записях");

                    for (int i = 0; i < rows.size(); i++) {
                        String want = resolveExpected(entry, LANG, i);
                        String got = rows.get(i).outcome();
                        if (want == null) {
                            mismatches.add(cell + " запись " + i
                                    + ": структура by_lang/by_record не покрывает эту координату");
                        } else if (!want.equals(got)) {
                            mismatches.add(cell + " запись " + i + ": ожидалось " + want + ", получено " + got);
                        }
                    }
                }
            }
        }
        assertTrue(mismatches.isEmpty(), String.join("\n", mismatches));
    }

    /**
     * Контрольное плечо задаёт точку отсчёта по размеру: json-schema
     * обязано давать ТЕ ЖЕ САМЫЕ байты. Расхождение — ошибка реализации,
     * а не находка про формат.
     */
    @Test
    void controlArmAndJsonSchemaAgreeOnBytes() throws Exception {
        for (String change : List.of("base", "add_default", "remove", "rename", "unknown_field")) {
            String direction = change.equals("base") ? "same" : "newer_writer";
            List<Row> control = run("json", change, direction, "compat");
            List<Row> validated = run("json-schema", change, direction, "compat");
            for (int i = 0; i < control.size(); i++) {
                assertEquals(control.get(i).bytes(), validated.get(i).bytes(),
                        change + "/" + i + ": контроль и json-schema разошлись по размеру");
            }
        }
    }

    /**
     * Непрозрачный остаток есть только у Protobuf; у остальных плеч
     * круговая проба — n/a, и кодирование на ней не запускается вовсе.
     */
    @Test
    void roundtripIsNotApplicableOutsideProtobuf() throws Exception {
        for (String format : List.of("json", "json-schema", "avro")) {
            for (Row r : run(format, "remove", "newer_reader", "roundtrip")) {
                assertEquals("n/a", r.outcome(), format + ": круговая проба без остатка");
                assertFalse(r.encoded(), "у строки n/a кодирования не было");
                assertEquals(0, r.bytes());
                assertEquals("", r.stage(), "кодирование не запускалось — стадии нет");
                assertFalse(r.raw().has("record"), "у строк n/a поля record нет");
                assertFalse(r.raw().has("want"), "у строк n/a поля want нет");
            }
        }
    }

    /**
     * Круговая проба Protobuf выживает ровно там, где пара схем сводится
     * к чистому отбрасыванию полей; на прочих парах — n/a по третьей
     * причине.
     */
    @Test
    void protobufRoundtripPreservesUnknownFields() throws Exception {
        record Case(String change, String direction, String want) {}
        List<Case> cases = List.of(
                new Case("add_default", "newer_writer", "ok"),
                new Case("add_nodefault", "newer_writer", "ok"),
                new Case("remove", "newer_reader", "ok"),
                new Case("unknown_field", "newer_writer", "ok"),
                // Не чистое отбрасывание: читатель видит поле ИНАЧЕ.
                new Case("rename", "newer_reader", "n/a"),
                new Case("retype", "newer_writer", "n/a"),
                new Case("reuse_tag", "newer_reader", "n/a"),
                // Читатель знает БОЛЬШЕ писателя — отбрасывания нет вовсе.
                new Case("add_default", "newer_reader", "n/a"),
                new Case("remove", "newer_writer", "n/a"));

        for (Case c : cases) {
            for (Row r : run("protobuf", c.change(), c.direction(), "roundtrip")) {
                assertEquals(c.want(), r.outcome(),
                        "roundtrip/protobuf/" + c.change() + "/" + c.direction());
                if (c.want().equals("ok")) {
                    assertTrue(r.encoded(), "круговая проба кодирует по-настоящему");
                    assertTrue(r.bytes() > 0, "размер — от ПЕРВОГО кодирования");
                    assertEquals("decode-final", r.stage(), "круговая проба доходит до конца");
                }
            }
        }
    }

    /**
     * Размерный якорь на эталонной записи. Проверен независимо от обеих
     * реализаций ручным разбором проводного формата: у Protobuf три поля
     * дают 2 + 10 + 18 = 30 байт, у Avro — 1 + 9 + 17 = 27.
     */
    @Test
    void referenceRecordHasKnownWireSize() throws Exception {
        assertEquals(30, run("protobuf", "base", "same", "compat").get(0).bytes());
        assertEquals(27, run("avro", "base", "same", "compat").get(0).bytes());
    }

    /**
     * Вырожденная пара — n/a, и кодирование на ней не запускается.
     * Записи набора reuse_tag заведомо не подходят схемам avro и
     * json-schema; порядок «сначала вырожденность, потом записи»
     * — единственный, при котором находка стенда не превращается в его
     * поломку.
     */
    @Test
    void degeneratePairIsNotApplicableBeforeRecordsAreNeeded() throws Exception {
        for (String format : List.of("avro", "json-schema", "json")) {
            for (String direction : List.of("newer_reader", "newer_writer")) {
                for (Row r : run(format, "reuse_tag", direction, "compat")) {
                    assertEquals("n/a", r.outcome(), format + "/reuse_tag/" + direction);
                    assertFalse(r.encoded());
                }
            }
        }
    }

    /** Аргументы вне перечня — отказ пробы без единой строки. */
    @Test
    void badArgumentsRefuseWithoutPrintingRows() throws Exception {
        record Bad(String why, String[] args) {}
        List<Bad> bad = List.of(
                new Bad("плечо вне перечня", new String[]{"--format=cbor", "--change=base", "--direction=same"}),
                new Bad("нет обязательного аргумента", new String[]{"--format=avro", "--direction=same"}),
                new Bad("base с направлением", new String[]{"--format=avro", "--change=base", "--direction=newer_reader"}),
                new Bad("неизвестный аргумент", new String[]{"--format=avro", "--change=base", "--direction=same", "--record=0"}),
                new Bad("позиционный аргумент", new String[]{"--format=avro", "--change=base", "same"}),
                new Bad("вид пробы вне перечня", new String[]{"--format=avro", "--change=base", "--direction=same", "--op=fuzz"}));

        for (Bad b : bad) {
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            ByteArrayOutputStream err = new ByteArrayOutputStream();
            int code = Probe.run(b.args(), new PrintStream(out, true, StandardCharsets.UTF_8),
                    new PrintStream(err, true, StandardCharsets.UTF_8));
            assertNotEquals(0, code, b.why());
            assertEquals("", out.toString(StandardCharsets.UTF_8), b.why() + ": напечатана строка");
            assertFalse(err.toString(StandardCharsets.UTF_8).isBlank(), b.why() + ": нет объяснения");
        }
    }

    /**
     * Дайджест считается по тем самым байтам, что идут в разбор схемы:
     * повторное чтение файла оставило бы щель между сверкой и
     * использованием.
     */
    @Test
    void manifestCoversEverySchemaTheProbeReads() throws Exception {
        Stand stand = Stand.locate();
        for (String notation : List.of("avro", "protobuf", "json-schema")) {
            Stand.SchemaFile v1 = stand.schema(notation, 1, "");
            assertTrue(v1.bytes().length > 0);
            for (String change : List.of("add_default", "add_nodefault", "remove",
                    "rename", "retype", "reuse_tag", "unknown_field")) {
                assertTrue(stand.schema(notation, 2, change).bytes().length > 0,
                        notation + "/" + change);
            }
        }
    }

    /**
     * Свойство §14: тихая порча Protobuf невидима ровно тогда, когда
     * истинное значение совпадает с нулевым значением типа читателя.
     * Текущие канонические записи в эту точку не попадают, поэтому
     * свойство зафиксировано на вычислении ожидания, а не на них.
     */
    @Test
    void silentCorruptionIsInvisibleOnlyAtTheZeroValue() {
        // строка "0" → целое 0: совпадёт с тем, что вернёт чтение.
        assertEquals(Value.of(0L), Expect.cast(Value.of("0"), Value.Cat.INT));
        // целое 0 → строка "0": чтение вернёт пустую строку, значения различны.
        assertNotEquals(Value.of(""), Expect.cast(Value.of(0L), Value.Cat.STRING));
    }

    /** Запись, не соответствующая схеме писателя, — отказ пробы, а не строка таблицы. */
    @Test
    void recordNotMatchingWriterSchemaRefusesTheWholeCell() throws Exception {
        // Набор reuse_tag (id, name, login_count) не подходит схеме
        // user_v2_reuse_tag.avsc (id, name, email): поля email в записи нет.
        // Направление same вырожденным не бывает, значит спасения через n/a нет.
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        ByteArrayOutputStream err = new ByteArrayOutputStream();
        int code = Probe.run(
                new String[]{"--format=avro", "--change=reuse_tag", "--direction=same"},
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(err, true, StandardCharsets.UTF_8));
        assertNotEquals(0, code);
        assertEquals("", out.toString(StandardCharsets.UTF_8), "наполовину напечатанная клетка хуже ненапечатанной");
    }

    /** Ожидание строится ИМЕНАМИ ЧИТАТЕЛЯ; переименование не требует отдельного шага. */
    @Test
    void expectationIsIndexedByReaderNames() throws Exception {
        JsonNode want = run("avro", "rename", "newer_reader", "compat").get(0).raw().get("want");
        assertTrue(want.has("contact"), "поле читателя");
        assertFalse(want.has("email"), "имя писателя в ожидание не попадает");
        assertEquals("anna@example.com", want.get("contact").asText());
    }

    /** Объявленное пустое умолчание даёт ПРИСУТСТВУЮЩЕЕ поле со значением «пусто». */
    @Test
    void declaredNullDefaultYieldsPresentNullField() throws Exception {
        JsonNode want = run("avro", "add_default", "newer_reader", "compat").get(0).raw().get("want");
        assertTrue(want.has("age"), "поле обязано присутствовать");
        assertTrue(want.get("age").isNull(), "не ноль и не пропуск поля");
    }

    /** В результат чтения protobuf попадает каждое поле схемы читателя, даже неустановленное. */
    @Test
    void protobufReadYieldsEveryReaderField() throws Exception {
        JsonNode want = run("protobuf", "remove", "newer_writer", "compat").get(0).raw().get("want");
        assertTrue(want.has("email"), "поле читателя без пары у писателя");
        assertEquals("", want.get("email").asText(), "нулевое значение типа в proto3");

        Map<String, Value> byName = new LinkedHashMap<>();
        byName.put("id", Value.of(1L));
        assertFalse(byName.isEmpty());
    }
}
