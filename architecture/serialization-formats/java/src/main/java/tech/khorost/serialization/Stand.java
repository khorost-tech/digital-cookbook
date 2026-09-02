package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.io.IOException;
import java.net.URISyntaxException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Каталог стенда: проба его УЗНАЁТ, а не выбирает.
 *
 * Ни путей, ни каталога аргументом нет. Пока пути приходили снаружи,
 * любую клетку можно было перевести в любой исход подставным каталогом
 * или плечом, не совпадающим с нотацией схем. Текущий рабочий каталог в
 * поиске не участвует по той же причине.
 */
final class Stand {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    /** Файл стенда вместе с ТЕМИ САМЫМИ байтами, по которым сверен дайджест. */
    record SchemaFile(String name, String notation, int version, String change, byte[] bytes) {}

    private final Path root;
    private final String algorithm;
    private final JsonNode files;

    private Stand(Path root, String algorithm, JsonNode files) {
        this.root = root;
        this.algorithm = algorithm;
        this.files = files;
    }

    Path schemasDir() { return root.resolve("schemas"); }

    static Stand locate() {
        Path start;
        try {
            start = Path.of(Stand.class.getProtectionDomain().getCodeSource()
                    .getLocation().toURI()).toAbsolutePath();
        } catch (URISyntaxException | NullPointerException e) {
            throw new Failures.ProbeRefusal("не удалось определить расположение исполняемого файла");
        }

        for (Path dir = Files.isDirectory(start) ? start : start.getParent();
             dir != null; dir = dir.getParent()) {
            Path manifest = dir.resolve("schemas").resolve("manifest.json");
            if (Files.isRegularFile(manifest)) {
                return load(dir, manifest);
            }
        }
        throw new Failures.ProbeRefusal(
                "каталог стенда не найден: ни в одном каталоге вверх от " + start
                        + " нет schemas/manifest.json");
    }

    private static Stand load(Path root, Path manifestPath) {
        JsonNode manifest;
        try {
            manifest = MAPPER.readTree(Files.readAllBytes(manifestPath));
        } catch (IOException e) {
            throw new Failures.ProbeRefusal("манифест не читается: " + manifestPath + " — " + e);
        }
        JsonNode files = manifest.get("files");
        JsonNode algorithm = manifest.get("algorithm");
        if (files == null || algorithm == null) {
            throw new Failures.ProbeRefusal("манифест без algorithm или files: " + manifestPath);
        }
        return new Stand(root, algorithm.asText(), files);
    }

    /** Нотация выводится из плеча: плечо и нотация схем — одна координата, а не две. */
    static String notationOf(String format) {
        return switch (format) {
            case "avro" -> "avro";
            case "protobuf" -> "protobuf";
            // Контрольное плечо схему не читает, но клетку надо чем-то
            // назвать, а вырожденность — чем-то определить. Разрешаем
            // координаты схемами того плеча, с которым контроль сравнивают
            // по размеру.
            case "json-schema", "json" -> "json-schema";
            default -> throw new Failures.ProbeRefusal("плечо вне перечня: " + format);
        };
    }

    /**
     * Поиск схемы по нотации, версии и изменению. Имена файлов проба не
     * разбирает: свойства файла берутся из записи манифеста.
     */
    SchemaFile schema(String notation, int version, String change) {
        List<String> found = new ArrayList<>();
        for (Iterator<String> it = files.fieldNames(); it.hasNext(); ) {
            String name = it.next();
            JsonNode entry = files.get(name);
            if (!"schema".equals(text(entry, "role"))) continue;
            if (!notation.equals(text(entry, "notation"))) continue;
            JsonNode v = entry.get("version");
            if (v == null || v.asInt() != version) continue;
            if (!change.equals(text(entry, "change"))) continue;
            found.add(name);
        }
        if (found.size() != 1) {
            throw new Failures.ProbeRefusal("в манифесте " + found.size() + " схем с нотацией "
                    + notation + ", версией " + version + " и изменением «" + change
                    + "» — должна быть ровно одна");
        }
        String name = found.get(0);
        byte[] bytes = readVerified(name);
        return new SchemaFile(name, notation, version, change, bytes);
    }

    /**
     * Канонические записи. Набор выбирается версией и изменением схемы
     * ПИСАТЕЛЯ, а не направлением: запись всегда имеет форму того, кто её
     * пишет.
     */
    List<Map<String, Value>> records(int writerVersion, String writerChange) {
        byte[] bytes = readVerified("records.json");
        JsonNode root;
        try {
            root = MAPPER.readTree(bytes);
        } catch (IOException e) {
            throw new Failures.ProbeRefusal("набор канонических записей не читается: " + e);
        }

        JsonNode set = root.get(writerVersion == 1 ? "v1" : "v2");
        if (set != null && writerVersion != 1) set = set.get(writerChange);
        JsonNode array = set == null ? null : set.get("records");
        if (array == null || !array.isArray() || array.isEmpty()) {
            throw new Failures.ProbeRefusal("в records.json нет непустого набора для версии "
                    + writerVersion + " и изменения «" + writerChange + "»");
        }

        List<Map<String, Value>> records = new ArrayList<>();
        for (JsonNode node : array) {
            Map<String, Value> record = new LinkedHashMap<>();
            node.fields().forEachRemaining(e -> record.put(e.getKey(), Value.ofJson(e.getValue())));
            records.add(record);
        }
        return records;
    }

    /**
     * Файл читается РОВНО ОДИН РАЗ, и дайджест считается по тем самым
     * байтам, которые пойдут дальше. Повторное чтение оставило бы между
     * сверкой и использованием щель, в которую однажды уже проехали
     * восемь прогонов из трёхсот.
     */
    private byte[] readVerified(String name) {
        JsonNode entry = files.get(name);
        if (entry == null) {
            throw new Failures.ProbeRefusal("файла " + name + " нет в манифесте — стенду он не принадлежит");
        }
        byte[] bytes;
        try {
            bytes = Files.readAllBytes(schemasDir().resolve(name));
        } catch (IOException e) {
            throw new Failures.ProbeRefusal("файл стенда не читается: " + name + " — " + e);
        }

        // Текстовые файлы приводятся к одним концам строк: без этого
        // манифест был бы верен ровно на одной операционной системе.
        byte[] hashed = "text".equals(text(entry, "content")) ? stripCarriageReturns(bytes) : bytes;
        String actual = digest(hashed);
        String expected = text(entry, "digest");
        if (!actual.equals(expected)) {
            throw new Failures.ProbeRefusal("дайджест " + name + " не совпал с манифестом: ожидался "
                    + expected + ", посчитан " + actual);
        }
        return bytes;
    }

    private static byte[] stripCarriageReturns(byte[] in) {
        byte[] out = new byte[in.length];
        int n = 0;
        for (int i = 0; i < in.length; i++) {
            if (in[i] == '\r' && i + 1 < in.length && in[i + 1] == '\n') continue;
            out[n++] = in[i];
        }
        byte[] result = new byte[n];
        System.arraycopy(out, 0, result, 0, n);
        return result;
    }

    private String digest(byte[] bytes) {
        try {
            MessageDigest md = MessageDigest.getInstance(switch (algorithm) {
                case "sha256" -> "SHA-256";
                default -> throw new Failures.ProbeRefusal("неизвестный алгоритм дайджеста: " + algorithm);
            });
            return HexFormat.of().formatHex(md.digest(bytes));
        } catch (NoSuchAlgorithmException e) {
            throw new Failures.ProbeRefusal("алгоритм дайджеста недоступен: " + algorithm);
        }
    }

    private static String text(JsonNode node, String field) {
        JsonNode v = node.get(field);
        return v == null ? null : v.asText();
    }

    static String utf8(byte[] bytes) { return new String(bytes, StandardCharsets.UTF_8); }
}
