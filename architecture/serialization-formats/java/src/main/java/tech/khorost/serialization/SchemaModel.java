package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.google.protobuf.DescriptorProtos;
import com.google.protobuf.Descriptors;
import org.apache.avro.JsonProperties;
import org.apache.avro.Schema;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Схема, приведённая к общему описанию полей, вместе с родным объектом
 * библиотеки, которым потом кодируют и читают.
 *
 * Разбор здесь — «подготовка схемы» в смысле спеки: всё, что тут падает,
 * это исход {@code error} (сломалась проба), а не {@code refused}
 * (формат отказался читать).
 */
final class SchemaModel {

    /** Сообщение, которое во всех Protobuf-схемах стенда одно и то же. */
    private static final String PROTO_MESSAGE = "tech.khorost.serialization.User";

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final String notation;
    private final List<FieldDesc> fields;
    private final Object nativeSchema;
    private final String text;

    private SchemaModel(String notation, List<FieldDesc> fields, Object nativeSchema, String text) {
        this.notation = notation;
        this.fields = fields;
        this.nativeSchema = nativeSchema;
        this.text = text;
    }

    String notation() { return notation; }

    /** Поля в том порядке, в котором их объявляет схема: порядок значим при поиске пары. */
    List<FieldDesc> fields() { return fields; }

    Schema avro() { return (Schema) nativeSchema; }

    Descriptors.Descriptor proto() { return (Descriptors.Descriptor) nativeSchema; }

    String jsonSchemaText() { return text; }

    static SchemaModel parse(Stand.SchemaFile file) {
        return switch (file.notation()) {
            case "avro" -> parseAvro(file);
            case "protobuf" -> parseProto(file);
            case "json-schema" -> parseJsonSchema(file);
            default -> throw new Failures.SchemaSetup(
                    "неизвестная нотация в манифесте: " + file.notation());
        };
    }

    /**
     * Структурное совпадение наборов полей — признак того, что заявленное
     * изменение в этой нотации не выражается никак.
     */
    boolean structurallySameAs(SchemaModel other) {
        if (!notation.equals(other.notation)) return false;
        List<String> a = fields.stream().map(FieldDesc::structuralKey)
                .sorted(Comparator.naturalOrder()).toList();
        List<String> b = other.fields.stream().map(FieldDesc::structuralKey)
                .sorted(Comparator.naturalOrder()).toList();
        return a.equals(b);
    }

    // ---------------------------------------------------------------- Avro

    private static SchemaModel parseAvro(Stand.SchemaFile file) {
        Schema schema;
        try {
            schema = new Schema.Parser().parse(new String(file.bytes(), StandardCharsets.UTF_8));
        } catch (RuntimeException e) {
            throw new Failures.SchemaSetup("Avro-схема не разобрана: " + file.name(), e);
        }
        if (schema.getType() != Schema.Type.RECORD) {
            throw new Failures.SchemaSetup("верхний уровень Avro-схемы не запись: " + file.name());
        }

        List<FieldDesc> fields = new ArrayList<>();
        for (Schema.Field f : schema.getFields()) {
            Schema branch = firstNonNullBranch(f.schema());
            boolean nullable = hasNullBranch(f.schema());

            Value.Cat cat = avroCategory(branch);
            int bits = switch (branch.getType()) {
                case INT -> 32;
                case LONG -> 64;
                default -> 0;
            };

            boolean hasDefault = f.hasDefaultValue();
            Value defaultValue = Value.NULL;
            if (hasDefault) {
                Object d = f.defaultVal();
                // Объявленное пустое умолчание приходит особым маркером,
                // а не как null: без этой ветви оно превратилось бы в
                // «неизвестную категорию».
                defaultValue = (d == JsonProperties.NULL_VALUE) ? Value.NULL : Value.of(d);
            }

            fields.add(new FieldDesc(f.name(), null, List.copyOf(f.aliases()), cat, bits,
                    hasDefault, defaultValue,
                    // Понятия «необязательное поле без умолчания» в Avro нет.
                    true, nullable));
        }
        return new SchemaModel("avro", List.copyOf(fields), schema, null);
    }

    private static Schema firstNonNullBranch(Schema s) {
        if (s.getType() != Schema.Type.UNION) return s;
        for (Schema branch : s.getTypes()) {
            if (branch.getType() != Schema.Type.NULL) return branch;
        }
        // Объединение из одних пустых веток: категория — «пусто».
        return s.getTypes().get(0);
    }

    private static boolean hasNullBranch(Schema s) {
        if (s.getType() == Schema.Type.NULL) return true;
        if (s.getType() != Schema.Type.UNION) return false;
        return s.getTypes().stream().anyMatch(b -> b.getType() == Schema.Type.NULL);
    }

    private static Value.Cat avroCategory(Schema s) {
        return switch (s.getType()) {
            case INT, LONG -> Value.Cat.INT;
            case STRING -> Value.Cat.STRING;
            case BOOLEAN -> Value.Cat.BOOL;
            case NULL -> Value.Cat.NULL;
            default -> Value.Cat.UNKNOWN;
        };
    }

    // ------------------------------------------------------------ Protobuf

    private static SchemaModel parseProto(Stand.SchemaFile file) {
        Descriptors.Descriptor message;
        try {
            DescriptorProtos.FileDescriptorSet set =
                    DescriptorProtos.FileDescriptorSet.parseFrom(file.bytes());
            Map<String, Descriptors.FileDescriptor> built = new HashMap<>();
            for (DescriptorProtos.FileDescriptorProto proto : set.getFileList()) {
                List<Descriptors.FileDescriptor> deps = new ArrayList<>();
                for (String dep : proto.getDependencyList()) {
                    Descriptors.FileDescriptor d = built.get(dep);
                    if (d == null) {
                        throw new Failures.SchemaSetup(
                                "в дескрипторе " + file.name() + " нет зависимости " + dep);
                    }
                    deps.add(d);
                }
                built.put(proto.getName(), Descriptors.FileDescriptor.buildFrom(
                        proto, deps.toArray(new Descriptors.FileDescriptor[0])));
            }
            message = built.values().stream()
                    .map(fd -> fd.findMessageTypeByName(PROTO_MESSAGE.substring(
                            PROTO_MESSAGE.lastIndexOf('.') + 1)))
                    .filter(d -> d != null && d.getFullName().equals(PROTO_MESSAGE))
                    .findFirst()
                    .orElseThrow(() -> new Failures.SchemaSetup(
                            "в дескрипторе " + file.name() + " нет сообщения " + PROTO_MESSAGE));
        } catch (Failures.SchemaSetup e) {
            throw e;
        } catch (Exception e) {
            throw new Failures.SchemaSetup("дескриптор не собран: " + file.name(), e);
        }

        List<FieldDesc> fields = new ArrayList<>();
        for (Descriptors.FieldDescriptor f : message.getFields()) {
            Value.Cat cat = protoCategory(f.getType());
            int bits = switch (f.getType()) {
                case INT32, SINT32, SFIXED32 -> 32;
                case INT64, SINT64, SFIXED64 -> 64;
                default -> 0;
            };
            // В proto3 у каждого поля есть умолчание, равное нулевому
            // значению его типа: формат не различает «нет значения» и
            // «пришёл ноль», и для ожидания это неотличимо от
            // объявленного умолчания.
            fields.add(new FieldDesc(f.getName(), f.getNumber(), List.of(), cat, bits,
                    true, Value.zeroOf(cat),
                    // Обязательных полей в proto3 не существует.
                    false, false));
        }
        return new SchemaModel("protobuf", List.copyOf(fields), message, null);
    }

    private static Value.Cat protoCategory(Descriptors.FieldDescriptor.Type type) {
        return switch (type) {
            case INT32, INT64, SINT32, SINT64, SFIXED32, SFIXED64 -> Value.Cat.INT;
            case STRING -> Value.Cat.STRING;
            case BOOL -> Value.Cat.BOOL;
            default -> Value.Cat.UNKNOWN;
        };
    }

    // --------------------------------------------------------- JSON Schema

    private static SchemaModel parseJsonSchema(Stand.SchemaFile file) {
        String text = new String(file.bytes(), StandardCharsets.UTF_8);
        JsonNode root;
        try {
            root = MAPPER.readTree(text);
        } catch (Exception e) {
            throw new Failures.SchemaSetup("JSON-схема не разобрана: " + file.name(), e);
        }

        // Обязательность берётся из собственного списка required, а не из
        // факта присутствия в properties: это разные вещи, и их смешение
        // однажды уже превратило законный ok в wrong.
        Map<String, Boolean> required = new LinkedHashMap<>();
        JsonNode req = root.get("required");
        if (req != null && req.isArray()) {
            req.forEach(n -> required.put(n.asText(), Boolean.TRUE));
        }

        List<FieldDesc> fields = new ArrayList<>();
        JsonNode props = root.get("properties");
        if (props != null) {
            props.fields().forEachRemaining(entry -> {
                JsonNode spec = entry.getValue();
                JsonNode typeNode = spec.get("type");
                Value.Cat cat = jsonCategory(typeNode == null ? null : typeNode.asText());
                boolean hasDefault = spec.has("default");
                Value defaultValue = hasDefault ? Value.ofJson(spec.get("default")) : Value.NULL;
                fields.add(new FieldDesc(entry.getKey(), null, List.of(), cat,
                        // Границ ширины схемы стенда не задают, значит
                        // ограничения по разрядности для этой нотации нет.
                        0, hasDefault, defaultValue,
                        required.containsKey(entry.getKey()), false));
            });
        }
        return new SchemaModel("json-schema", List.copyOf(fields), root, text);
    }

    private static Value.Cat jsonCategory(String type) {
        if (type == null) return Value.Cat.UNKNOWN;
        return switch (type) {
            case "integer" -> Value.Cat.INT;
            case "string" -> Value.Cat.STRING;
            case "boolean" -> Value.Cat.BOOL;
            case "null" -> Value.Cat.NULL;
            default -> Value.Cat.UNKNOWN;
        };
    }
}
