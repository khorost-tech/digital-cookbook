package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.google.protobuf.DynamicMessage;
import com.google.protobuf.Descriptors;
import com.google.protobuf.InvalidProtocolBufferException;
// Классы com.networknt.schema.Schema и .Error не импортируются: первый
// столкнулся бы с org.apache.avro.Schema, второй — с java.lang.Error.
import com.networknt.schema.InputFormat;
import com.networknt.schema.SchemaRegistry;
import com.networknt.schema.SpecificationVersion;
import org.apache.avro.Schema;
import org.apache.avro.generic.GenericData;
import org.apache.avro.generic.GenericDatumReader;
import org.apache.avro.generic.GenericDatumWriter;
import org.apache.avro.generic.GenericRecord;
import org.apache.avro.io.BinaryEncoder;
import org.apache.avro.io.DecoderFactory;
import org.apache.avro.io.EncoderFactory;

import java.io.ByteArrayOutputStream;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;

/**
 * Плечи стенда. Здесь производятся исходы во всех клетках таблицы:
 * реализация, сделавшая тут «как естественнее», получит другую таблицу
 * при том же вычислении ожидания — и предъявит своё расхождение как
 * расхождение форматов.
 */
abstract class Codec {

    static Codec of(String format, SchemaModel writer, SchemaModel reader) {
        return switch (format) {
            case "json" -> new JsonCodec(null, null);
            case "json-schema" -> new JsonCodec(writer, reader);
            case "avro" -> new AvroCodec(writer, reader);
            case "protobuf" -> new ProtobufCodec(writer, reader);
            default -> throw new Failures.ProbeRefusal("плечо вне перечня: " + format);
        };
    }

    /** Кодирование схемой писателя. */
    abstract byte[] encode(Map<String, Value> record);

    /** Чтение схемой читателя. */
    abstract Map<String, Value> decode(byte[] bytes);

    /**
     * Есть ли у плеча непрозрачный остаток — байты, которые схема
     * читателя не описывает, но которые формат умеет пронести через
     * декодирование и приписать обратно. Есть только у Protobuf.
     */
    boolean hasOpaqueRemainder() { return false; }

    /** Чтение схемой читателя С СОХРАНЕНИЕМ остатка. */
    Object decodeKeepingRemainder(byte[] bytes) {
        throw new UnsupportedOperationException("у плеча нет непрозрачного остатка");
    }

    /** Повторная запись СХЕМОЙ ЧИТАТЕЛЯ вместе с сохранённым остатком. */
    byte[] encodeState(Object state) {
        throw new UnsupportedOperationException("у плеча нет непрозрачного остатка");
    }

    /** Заключительное чтение схемой ПИСАТЕЛЯ. */
    Map<String, Value> decodeWithWriter(byte[] bytes) {
        throw new UnsupportedOperationException("у плеча нет непрозрачного остатка");
    }

    // ------------------------------------------------- json и json-schema

    /**
     * Контрольное плечо и json-schema дают ОДНИ И ТЕ ЖЕ байты: вся
     * разница в проверках, и их две — по схеме писателя при записи и по
     * схеме читателя при чтении.
     *
     * Проверка при чтении — не украшение, а половина колонки: именно она
     * даёт отказы там, где схема читателя запрещает не объявленные ею
     * свойства, а в байтах писателя такое свойство есть.
     */
    private static final class JsonCodec extends Codec {
        private static final ObjectMapper MAPPER = new ObjectMapper();

        private final com.networknt.schema.Schema writerSchema;
        private final com.networknt.schema.Schema readerSchema;

        JsonCodec(SchemaModel writer, SchemaModel reader) {
            this.writerSchema = compile(writer);
            this.readerSchema = compile(reader);
        }

        private static com.networknt.schema.Schema compile(SchemaModel model) {
            if (model == null) return null;  // контрольное плечо схему не читает вовсе
            try {
                return SchemaRegistry.withDefaultDialect(SpecificationVersion.DRAFT_2020_12)
                        .getSchema(model.jsonSchemaText());
            } catch (RuntimeException e) {
                throw new Failures.SchemaSetup("JSON-схема не скомпилирована", e);
            }
        }

        @Override
        byte[] encode(Map<String, Value> record) {
            ObjectNode node = MAPPER.createObjectNode();
            for (Map.Entry<String, Value> e : record.entrySet()) {
                put(node, e.getKey(), e.getValue());
            }
            byte[] bytes;
            try {
                bytes = MAPPER.writeValueAsBytes(node);
            } catch (Exception e) {
                throw new Failures.FormatRefusal("запись не сериализуется в JSON: " + e);
            }
            // Байты у контроля и у json-schema обязаны быть ОДНИ И ТЕ ЖЕ,
            // поэтому сериализация одна, а проверка добавляется поверх.
            if (writerSchema != null) {
                validate(writerSchema, new String(bytes, StandardCharsets.UTF_8), "схема писателя");
            }
            return bytes;
        }

        @Override
        Map<String, Value> decode(byte[] bytes) {
            JsonNode node;
            try {
                node = MAPPER.readTree(bytes);
            } catch (Exception e) {
                throw new Failures.FormatRefusal("байты не разбираются как JSON: " + e);
            }
            // Проверяется РАЗОБРАННЫЙ РЕЗУЛЬТАТ. Проекции полей у этого
            // плеча нет: прочитанное отдаётся как есть, без попытки
            // подогнать его под схему читателя.
            if (readerSchema != null) {
                validate(readerSchema, new String(bytes, StandardCharsets.UTF_8), "схема читателя");
            }

            Map<String, Value> record = new LinkedHashMap<>();
            node.fields().forEachRemaining(e -> record.put(e.getKey(), Value.ofJson(e.getValue())));
            return record;
        }

        private static void validate(com.networknt.schema.Schema schema, String json, String which) {
            List<com.networknt.schema.Error> errors = schema.validate(json, InputFormat.JSON);
            if (!errors.isEmpty()) {
                throw new Failures.FormatRefusal(which + " отвергла запись: "
                        + errors.stream().map(com.networknt.schema.Error::getMessage)
                        .collect(Collectors.joining("; ")));
            }
        }

        private static void put(ObjectNode node, String name, Value v) {
            switch (v.cat()) {
                case INT -> node.put(name, v.asLong());
                case STRING -> node.put(name, v.asString());
                case BOOL -> node.put(name, (Boolean) v.raw());
                case NULL -> node.putNull(name);
                case UNKNOWN -> node.set(name, MAPPER.valueToTree(v.raw()));
                // RECORD (задача 6bis, retype_message): вложенный объект
                // пишется РЕКУРСИВНО тем же put — JSON/JSON Schema не
                // нуждаются в отдельном представлении вложенной записи,
                // это обычный объект внутри объекта.
                case RECORD -> {
                    ObjectNode nested = MAPPER.createObjectNode();
                    for (Map.Entry<String, Value> e : v.asRecord().entrySet()) {
                        put(nested, e.getKey(), e.getValue());
                    }
                    node.set(name, nested);
                }
            }
        }
    }

    // ---------------------------------------------------------------- Avro

    /**
     * Запись — схемой писателя, без записи схемы в поток: схему передают
     * отдельно, как при работе через реестр, а не как в контейнерном
     * файле.
     *
     * Чтение — через разрешение схемы читателя против схемы писателя.
     * Своей проверки совместимости здесь нет намеренно: её ответ был бы
     * нашим, а не Avro. Неудача разрешения — отказ ФОРМАТА.
     */
    private static final class AvroCodec extends Codec {
        private final Schema writer;
        private final Schema reader;

        AvroCodec(SchemaModel writer, SchemaModel reader) {
            this.writer = writer.avro();
            this.reader = reader.avro();
        }

        @Override
        byte[] encode(Map<String, Value> record) {
            GenericData.Record row = new GenericData.Record(writer);
            for (Schema.Field f : writer.getFields()) {
                row.put(f.name(), coerce(f.schema(), record.get(f.name())));
            }
            try {
                ByteArrayOutputStream out = new ByteArrayOutputStream();
                BinaryEncoder encoder = EncoderFactory.get().binaryEncoder(out, null);
                new GenericDatumWriter<GenericRecord>(writer).write(row, encoder);
                encoder.flush();
                return out.toByteArray();
            } catch (Exception e) {
                throw new Failures.FormatRefusal("Avro не записал запись: " + e, e);
            }
        }

        @Override
        Map<String, Value> decode(byte[] bytes) {
            GenericRecord row;
            try {
                GenericDatumReader<GenericRecord> datumReader = new GenericDatumReader<>(writer, reader);
                row = datumReader.read(null, DecoderFactory.get().binaryDecoder(bytes, null));
            } catch (Exception e) {
                throw new Failures.FormatRefusal("Avro отказался читать: " + e, e);
            }
            Map<String, Value> record = new LinkedHashMap<>();
            for (Schema.Field f : reader.getFields()) {
                record.put(f.name(), valueOfAvro(row.get(f.name())));
            }
            return record;
        }

        /**
         * Value.of(Object) не знает про Avro-специфичный GenericRecord —
         * задача 6bis (retype_message): вложенная запись раскрывается
         * РЕКУРСИВНО в Value.RECORD той же функцией, а не отдаётся как
         * сырой GenericRecord через общий Value.of (тот ушёл бы в
         * Cat.UNKNOWN и не сравнивался бы структурно со значением "want").
         */
        private static Value valueOfAvro(Object raw) {
            if (raw instanceof GenericRecord gr) {
                Map<String, Value> nested = new LinkedHashMap<>();
                for (Schema.Field f : gr.getSchema().getFields()) {
                    nested.put(f.name(), valueOfAvro(gr.get(f.name())));
                }
                return Value.ofRecord(nested);
            }
            return Value.of(raw);
        }

        /**
         * Ветвь объединения выбирается по значению только ЗДЕСЬ, при
         * записи конкретного значения; описание поля (§6.3) при этом
         * по-прежнему берёт первую не-пустую ветвь.
         */
        private static Object coerce(Schema schema, Value v) {
            Schema target = schema;
            if (schema.getType() == Schema.Type.UNION) {
                if (v.cat() == Value.Cat.NULL) return null;
                for (Schema branch : schema.getTypes()) {
                    if (branch.getType() != Schema.Type.NULL) { target = branch; break; }
                }
            }
            return switch (target.getType()) {
                case INT -> (int) v.asLong();
                case LONG -> v.asLong();
                case STRING -> v.asString();
                case BOOLEAN -> v.raw();
                case NULL -> null;
                // retype_message (задача 6bis): вложенная запись
                // приходит как Value.RECORD (Map<String, Value>) — тем
                // же способом, каким устроена запись верхнего уровня.
                // GenericDatumWriter ТРЕБУЕТ настоящий GenericRecord для
                // полей типа RECORD — сырой java.util.Map он не
                // принимает (ClassCastException "cannot be cast to
                // expected type"), в отличие от Go/hamba-avro, который
                // строит значение по любому generic-представлению через
                // отражение по Go-типу. Собирается ОТДЕЛЬНЫМ
                // GenericData.Record по схеме вложенного типа,
                // рекурсивно тем же coerce.
                case RECORD -> {
                    Map<String, Value> nested = v.asRecord();
                    GenericData.Record sub = new GenericData.Record(target);
                    for (Schema.Field sf : target.getFields()) {
                        Value sv = nested.get(sf.name());
                        if (sv != null) sub.put(sf.name(), coerce(sf.schema(), sv));
                    }
                    yield sub;
                }
                default -> v.raw();
            };
        }
    }

    // ------------------------------------------------------------ Protobuf

    /**
     * Работа идёт через дескрипторы и динамические сообщения, а не через
     * сгенерированный код: сгенерированный под конкретную версию схемы
     * код незаметно подменил бы измеряемое — сохранение неизвестных полей
     * стало бы свойством генератора.
     */
    private static final class ProtobufCodec extends Codec {
        private final Descriptors.Descriptor writer;
        private final Descriptors.Descriptor reader;

        ProtobufCodec(SchemaModel writer, SchemaModel reader) {
            this.writer = writer.proto();
            this.reader = reader.proto();
        }

        @Override
        byte[] encode(Map<String, Value> record) {
            DynamicMessage.Builder builder = DynamicMessage.newBuilder(writer);
            for (Descriptors.FieldDescriptor f : writer.getFields()) {
                Value v = record.get(f.getName());
                if (v == null || v.cat() == Value.Cat.NULL) continue;
                builder.setField(f, coerce(f, v));
            }
            return builder.build().toByteArray();
        }

        @Override
        Map<String, Value> decode(byte[] bytes) {
            return toRecord(parse(reader, bytes), reader);
        }

        @Override
        boolean hasOpaqueRemainder() { return true; }

        @Override
        Object decodeKeepingRemainder(byte[] bytes) {
            // Разбор в динамическое сообщение схемы читателя БЕЗ
            // отбрасывания неизвестных полей: они остаются в сообщении,
            // и это же делает возможной круговую пробу.
            return parse(reader, bytes);
        }

        @Override
        byte[] encodeState(Object state) {
            return ((DynamicMessage) state).toByteArray();
        }

        @Override
        Map<String, Value> decodeWithWriter(byte[] bytes) {
            return toRecord(parse(writer, bytes), writer);
        }

        private static DynamicMessage parse(Descriptors.Descriptor descriptor, byte[] bytes) {
            try {
                return DynamicMessage.parseFrom(descriptor, bytes);
            } catch (InvalidProtocolBufferException e) {
                throw new Failures.FormatRefusal("Protobuf отказался разбирать байты: " + e, e);
            }
        }

        /**
         * В результат чтения попадает КАЖДОЕ поле схемы, даже если оно не
         * было установлено: в proto3 значением незаполненного поля
         * является нулевое значение его типа. Взять только установленные
         * поля — выбор естественный и неверный: он перевернул бы три
         * клетки из ok в wrong.
         *
         * Поле типа MESSAGE (задача 6bis, retype_message) раскрывается
         * РЕКУРСИВНО в Value.RECORD той же функцией — а не отдаётся как
         * сырой DynamicMessage через Value.of(Object): тот ушёл бы в
         * Cat.UNKNOWN, не сравнивался бы структурно со значением "want"
         * (обычной картой из JSON-записи) и не сериализовался бы
         * безопасно в JSON-строку результата.
         */
        private static Map<String, Value> toRecord(DynamicMessage message,
                                                   Descriptors.Descriptor descriptor) {
            Map<String, Value> record = new LinkedHashMap<>();
            for (Descriptors.FieldDescriptor f : descriptor.getFields()) {
                Object raw = message.getField(f);
                if (f.getType() == Descriptors.FieldDescriptor.Type.MESSAGE && raw instanceof DynamicMessage sub) {
                    record.put(f.getName(), Value.ofRecord(toRecord(sub, f.getMessageType())));
                } else {
                    record.put(f.getName(), Value.of(raw));
                }
            }
            return record;
        }

        private static Object coerce(Descriptors.FieldDescriptor f, Value v) {
            return switch (f.getType()) {
                case INT32, SINT32, SFIXED32, UINT32, FIXED32 -> (int) v.asLong();
                case INT64, SINT64, SFIXED64, UINT64, FIXED64 -> v.asLong();
                case STRING -> v.asString();
                case BOOL -> v.raw();
                // retype_message: значение вложенного сообщения стенд
                // несёт как Value.RECORD (Map<String, Value>) — тем же
                // способом, каким представлена запись верхнего уровня.
                // Собирается ОТДЕЛЬНЫМ DynamicMessage.Builder по
                // дескриптору вложенного типа, рекурсивно тем же coerce.
                case MESSAGE -> {
                    Map<String, Value> nested = v.asRecord();
                    Descriptors.Descriptor subDescriptor = f.getMessageType();
                    DynamicMessage.Builder sub = DynamicMessage.newBuilder(subDescriptor);
                    for (Descriptors.FieldDescriptor sf : subDescriptor.getFields()) {
                        Value sv = nested.get(sf.getName());
                        if (sv == null || sv.cat() == Value.Cat.NULL) continue;
                        sub.setField(sf, coerce(sf, sv));
                    }
                    yield sub.build();
                }
                default -> v.raw();
            };
        }
    }
}
