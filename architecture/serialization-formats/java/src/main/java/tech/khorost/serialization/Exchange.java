package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.Base64;
import java.util.HexFormat;

/**
 * Обмен байтами между двумя реализациями пробы через файлы рабочего
 * каталога прогона (Задача 8: ось перекрёстного чтения — schemas/spec.md,
 * раздел «Ось 4»).
 *
 * Одна реализация не может декодировать байты, которые закодировала
 * другая, внутри одного процесса: перекрёстная проверка требует, чтобы
 * одна реализация ЗАПИСАЛА байты на диск, а другая — независимо от
 * первой — их ПРОЧИТАЛА. Каталог обмена не аргумент пробы (§4.2 не
 * нарушается): его выбирает вызывающий сценарий (в проде — текущий
 * рабочий каталог процесса), а имя файла внутри него полностью выводится
 * из координат клетки, номера записи и языка-писателя.
 *
 * Три ловушки, которые обязан ловить этот класс (решение контроллера
 * Задачи 8, то же самое решение, что и в Go-части —
 * internal/exchange/exchange.go, — оба обязаны совпадать по имени файла
 * и форме конверта, иначе писатель и читатель не найдут друг друга):
 * перепутанное имя (координаты внутри файла не совпадают с запрошенными),
 * недописанный или испорченный файл (дайджест не сходится с содержимым),
 * и след прошлого прогона — это уже забота вызывающего сценария, каталог
 * обмена обязан быть очищен ДО прогона, класс сам не отличит стухший файл
 * от свежего с теми же координатами.
 */
final class Exchange {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private Exchange() {}

    /**
     * Детерминированное имя файла обмена: функция только от координат
     * клетки, номера записи и языка-писателя. Обязано давать то же самое
     * имя, что и Go-часть, на тех же аргументах.
     */
    static Path crossFileName(Path dir, String lang, String format, String change, String direction, int recordIndex) {
        String name = String.format("cross_%s_%s_%s_%s_%d.json", lang, format, change, direction, recordIndex);
        return dir.resolve(name);
    }

    /**
     * Пишет конверт АТОМАРНО: сначала во временный файл того же
     * каталога, затем переименовывает. Обрыв процесса посередине записи
     * не оставит недописанный файл ПОД ОЖИДАЕМЫМ ИМЕНЕМ — readCross либо
     * увидит целый файл, либо не увидит никакого.
     */
    static void writeCross(Path dir, String lang, String format, String change, String direction,
                            int recordIndex, byte[] bytes) {
        ObjectNode env = MAPPER.createObjectNode();
        env.put("lang", lang);
        env.put("format", format);
        env.put("change", change);
        env.put("direction", direction);
        env.put("record_index", recordIndex);
        env.put("sha256", sha256Hex(bytes));
        env.put("bytes_b64", Base64.getEncoder().encodeToString(bytes));

        Path target = crossFileName(dir, lang, format, change, direction, recordIndex);
        Path tmp = null;
        try {
            Files.createDirectories(dir);
            tmp = Files.createTempFile(dir, "cross-", ".tmp");
            Files.write(tmp, MAPPER.writeValueAsBytes(env));
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (IOException e) {
            if (tmp != null) {
                try {
                    Files.deleteIfExists(tmp);
                } catch (IOException ignore) {
                    // Лучшее из возможного — сам временный файл уже не
                    // тот артефакт, который проверяет остальной класс.
                }
            }
            throw new Failures.ProbeRefusal("exchange: запись файла обмена " + target + ": " + e);
        }
    }

    /**
     * Читает файл обмена по детерминированному имени и возвращает байты
     * только после того, как:
     * <ol>
     * <li>файл найден и разобран как конверт;</li>
     * <li>координаты ВНУТРИ конверта совпадают с запрошенными (лечит
     *     перепутанное имя);</li>
     * <li>дайджест содержимого совпадает с зафиксированным при записи
     *     (лечит недописанный или испорченный файл).</li>
     * </ol>
     * Файл обмена — единственная связь между двумя независимыми
     * процессами, и она не защищена ничем, кроме этой проверки.
     */
    static byte[] readCross(Path dir, String lang, String format, String change, String direction, int recordIndex) {
        Path path = crossFileName(dir, lang, format, change, direction, recordIndex);
        byte[] raw;
        try {
            raw = Files.readAllBytes(path);
        } catch (IOException e) {
            throw new Failures.ProbeRefusal("exchange: файл обмена " + path + " не прочитан: " + e);
        }
        JsonNode env;
        try {
            env = MAPPER.readTree(raw);
        } catch (IOException e) {
            throw new Failures.ProbeRefusal("exchange: файл обмена " + path
                    + " повреждён: не разбирается как JSON: " + e);
        }
        String gotLang = text(env, "lang");
        String gotFormat = text(env, "format");
        String gotChange = text(env, "change");
        String gotDirection = text(env, "direction");
        int gotIndex = env.has("record_index") ? env.get("record_index").asInt() : Integer.MIN_VALUE;
        if (!lang.equals(gotLang) || !format.equals(gotFormat) || !change.equals(gotChange)
                || !direction.equals(gotDirection) || recordIndex != gotIndex) {
            throw new Failures.ProbeRefusal("exchange: координаты внутри файла обмена " + path
                    + " не совпадают с запрошенными (лежит: lang=" + gotLang + " format=" + gotFormat
                    + " change=" + gotChange + " direction=" + gotDirection + " record_index=" + gotIndex
                    + "; ожидались: lang=" + lang + " format=" + format + " change=" + change
                    + " direction=" + direction + " record_index=" + recordIndex
                    + ") — файл подменён или взят не тот");
        }
        byte[] bytes;
        try {
            bytes = Base64.getDecoder().decode(text(env, "bytes_b64"));
        } catch (IllegalArgumentException | NullPointerException e) {
            throw new Failures.ProbeRefusal("exchange: файл обмена " + path
                    + " повреждён: base64 не разбирается: " + e);
        }
        String expected = text(env, "sha256");
        String actual = sha256Hex(bytes);
        if (!actual.equals(expected)) {
            throw new Failures.ProbeRefusal("exchange: файл обмена " + path
                    + " повреждён или недописан: дайджест не совпадает (записан " + expected
                    + ", посчитан " + actual + ")");
        }
        return bytes;
    }

    private static String text(JsonNode node, String field) {
        JsonNode v = node.get(field);
        return v == null ? null : v.asText();
    }

    private static String sha256Hex(byte[] data) {
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(data));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }
}
