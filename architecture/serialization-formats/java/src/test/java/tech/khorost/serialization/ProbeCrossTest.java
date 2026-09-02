package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.ByteArrayOutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Задача 8, ось перекрёстного чтения: cross-emit/cross-accept/identity
 * на уровне всей пробы (Probe.run), не только Exchange.
 *
 * Каталог обмена передаётся тестовым оверлоадом Probe.run(..., Path)
 * явно: настоящий CLI-вызов запускает отдельную JVM на координату и
 * полагается на текущий рабочий каталог процесса (spec.md §17.2), а
 * внутри одного тестового процесса JVM не "перейти" в другой каталог.
 */
class ProbeCrossTest {

    private static final ObjectMapper M = new ObjectMapper();

    private record Row(int code, List<JsonNode> rows, String err) {}

    private static Row run(Path exchangeDir, String... args) throws Exception {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        ByteArrayOutputStream err = new ByteArrayOutputStream();
        int code = Probe.run(args,
                new PrintStream(out, true, StandardCharsets.UTF_8),
                new PrintStream(err, true, StandardCharsets.UTF_8),
                exchangeDir);
        List<JsonNode> rows = new ArrayList<>();
        String text = out.toString(StandardCharsets.UTF_8).strip();
        if (!text.isEmpty()) {
            for (String line : text.split("\\R")) {
                rows.add(M.readTree(line));
            }
        }
        return new Row(code, rows, err.toString(StandardCharsets.UTF_8));
    }

    @Test
    void crossAcceptRequiresWriterLang(@TempDir Path dir) throws Exception {
        for (String bad : new String[]{null, "python", "GO"}) {
            List<String> args = new ArrayList<>(List.of(
                    "--format=avro", "--change=rename", "--direction=newer_reader", "--op=cross-accept"));
            if (bad != null) args.add("--writer-lang=" + bad);
            Row r = run(dir, args.toArray(new String[0]));
            assertNotEquals(0, r.code(), "--writer-lang=" + bad + ": ожидали отказ");
        }
    }

    @Test
    void writerLangOnlyMeaningfulForCrossAccept(@TempDir Path dir) throws Exception {
        for (String op : List.of("compat", "roundtrip", "size", "cross-emit", "identity")) {
            String direction = (op.equals("size") || op.equals("identity")) ? "same" : "newer_reader";
            Row r = run(dir, "--format=avro", "--change=rename", "--direction=" + direction,
                    "--op=" + op, "--writer-lang=go");
            assertNotEquals(0, r.code(), "--op=" + op + ": --writer-lang не имеет смысла тут");
        }
    }

    /**
     * «Своя же контрольная клетка»: писатель и читатель — оба Java (эта
     * реализация — единственная, доступная тесту), поэтому обмен через
     * файл обязан дать построчно то же самое, что и обычная compat-проба
     * на тех же координатах (решение контроллера Задачи 8: расхождение
     * тут — Critical).
     */
    @Test
    void crossEmitThenAcceptMatchesCompatControl(@TempDir Path dir) throws Exception {
        record Coord(String format, String change, String direction) {}
        List<Coord> coords = List.of(
                new Coord("avro", "add_default", "newer_reader"),
                new Coord("avro", "alias_conflict", "newer_reader"),
                new Coord("protobuf", "unknown_field", "newer_writer"));

        for (Coord c : coords) {
            Row emit = run(dir, "--format=" + c.format(), "--change=" + c.change(),
                    "--direction=" + c.direction(), "--op=cross-emit");
            assertEquals(0, emit.code(), c + ": cross-emit: " + emit.err());
            assertTrue(emit.rows().isEmpty(), c + ": cross-emit не должен ничего печатать");

            Row cross = run(dir, "--format=" + c.format(), "--change=" + c.change(),
                    "--direction=" + c.direction(), "--op=cross-accept", "--writer-lang=java");
            assertEquals(0, cross.code(), c + ": cross-accept: " + cross.err());

            Row compat = run(dir, "--format=" + c.format(), "--change=" + c.change(),
                    "--direction=" + c.direction(), "--op=compat");
            assertEquals(0, compat.code(), c + ": compat: " + compat.err());

            assertEquals(compat.rows().size(), cross.rows().size(), c + ": разное число строк");
            for (int i = 0; i < cross.rows().size(); i++) {
                JsonNode cr = cross.rows().get(i);
                JsonNode pr = compat.rows().get(i);
                assertEquals("cross", cr.get("kind").asText(), c + " запись " + i);
                assertEquals("java", cr.get("writer").asText(), c + " запись " + i);
                assertEquals("java", cr.get("reader").asText(), c + " запись " + i);
                assertEquals(pr.get("outcome").asText(), cr.get("outcome").asText(),
                        c + " запись " + i + ": cross и compat разошлись — контроль провален");
                assertEquals(pr.get("bytes").asLong(), cr.get("bytes").asLong(), c + " запись " + i);
            }
        }
    }

    @Test
    void crossAcceptFailsWithoutPriorEmit(@TempDir Path dir) throws Exception {
        Row r = run(dir, "--format=avro", "--change=rename", "--direction=newer_reader",
                "--op=cross-accept", "--writer-lang=go");
        assertNotEquals(0, r.code(), "файла обмена от go никто не писал");
        assertTrue(r.rows().isEmpty());
    }

    @Test
    void identityRequiresSameDirection(@TempDir Path dir) throws Exception {
        for (String direction : List.of("newer_reader", "newer_writer")) {
            Row r = run(dir, "--format=avro", "--change=add_default", "--direction=" + direction, "--op=identity");
            assertNotEquals(0, r.code(), "направление " + direction + ": идентичность меряется на одной схеме");
        }
    }

    @Test
    void identityControlEqualForAllFormats(@TempDir Path dir) throws Exception {
        for (String format : List.of("json", "json-schema", "avro", "protobuf")) {
            Row r = run(dir, "--format=" + format, "--change=base", "--direction=same", "--op=identity");
            assertEquals(0, r.code(), format + ": " + r.err());
            assertEquals(1, r.rows().size(), format);
            JsonNode row = r.rows().get(0);
            assertEquals("identity-probe", row.get("kind").asText(), format);
            assertTrue(row.get("control_equal").asBoolean(), format + ": контроль не зелёный");
            assertFalse(row.get("sha256").asText().isEmpty(), format);
            assertTrue(row.get("bytes").asLong() > 0, format);
        }
    }
}
