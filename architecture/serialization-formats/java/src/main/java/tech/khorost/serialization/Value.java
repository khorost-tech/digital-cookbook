package tech.khorost.serialization;

import com.fasterxml.jackson.databind.JsonNode;

import java.math.BigDecimal;
import java.math.BigInteger;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;

/**
 * Значение поля: категория плюс само значение.
 *
 * Категория здесь не украшение. Стенд сравнивает записи, полученные
 * РАЗНЫМИ библиотеками, и без явной категории сравнение унаследовало бы
 * систему типов Java: Integer(1).equals(Long(1)) — ложь, а по правилам
 * стенда это одно и то же значение; «пусто» и нулевое значение типа —
 * наоборот, разные вещи, хотя в Java их легко смешать через null.
 */
public final class Value {

    // RECORD — задача 6bis (retype_message): значение вложенного
    // сообщения/записи, представленное РЕКУРСИВНО как Map<String, Value>
    // — тем же способом, каким представлена запись верхнего уровня. Без
    // отдельной категории значение такого поля падало в UNKNOWN и несло
    // сырой протообразный объект (DynamicMessage) — тот не сравнивался
    // бы структурно со значением "want" (обычной картой из JSON) и не
    // сериализовался бы безопасно в JSON-строку результата.
    public enum Cat { INT, STRING, BOOL, NULL, UNKNOWN, RECORD }

    /** «Пусто» — категория, а не отсутствие. Отсутствие поля выражается отсутствием ключа. */
    public static final Value NULL = new Value(Cat.NULL, null);

    private final Cat cat;
    private final Object raw;

    private Value(Cat cat, Object raw) {
        this.cat = cat;
        this.raw = raw;
    }

    public Cat cat() { return cat; }

    /** Значение как long; осмысленно только для категории INT. */
    public long asLong() { return (Long) raw; }

    public String asString() { return (String) raw; }

    public Object raw() { return raw; }

    /** Значение как вложенная запись; осмысленно только для категории RECORD. */
    @SuppressWarnings("unchecked")
    public Map<String, Value> asRecord() { return (Map<String, Value>) raw; }

    /** Строит значение категории RECORD из уже готовой карты полей. */
    public static Value ofRecord(Map<String, Value> fields) {
        return new Value(Cat.RECORD, fields);
    }

    /**
     * Нормализация по одному правилу для всех источников: и для
     * канонической записи, и для того, что вернула библиотека. Две копии
     * этого правила неизбежно разъехались бы незаметно, поэтому вход
     * здесь один.
     */
    public static Value of(Object o) {
        switch (o) {
            case null -> { return NULL; }
            case Value v -> { return v; }
            case Boolean b -> { return new Value(Cat.BOOL, b); }
            // Utf8 — родной строковый тип Avro; utf8.equals("Анна") даёт
            // false, поэтому приводим здесь, а не в месте сравнения.
            case CharSequence s -> { return new Value(Cat.STRING, s.toString()); }
            case Byte b -> { return new Value(Cat.INT, b.longValue()); }
            case Short s -> { return new Value(Cat.INT, s.longValue()); }
            case Integer i -> { return new Value(Cat.INT, i.longValue()); }
            case Long l -> { return new Value(Cat.INT, l); }
            case BigInteger bi -> { return fromBigDecimal(new BigDecimal(bi)); }
            case BigDecimal bd -> { return fromBigDecimal(bd); }
            case Float f -> { return fromBigDecimal(BigDecimal.valueOf(f.doubleValue())); }
            case Double d -> { return fromBigDecimal(BigDecimal.valueOf(d)); }
            default -> { return new Value(Cat.UNKNOWN, o); }
        }
    }

    /** Нормализация значения из JSON — то же правило, другой вход. */
    public static Value ofJson(JsonNode n) {
        if (n == null || n.isNull()) return NULL;
        if (n.isBoolean()) return new Value(Cat.BOOL, n.booleanValue());
        if (n.isTextual()) return new Value(Cat.STRING, n.textValue());
        if (n.isNumber()) return fromBigDecimal(n.decimalValue());
        // Вложенная запись — задача 6bis (retype_message): единственное
        // изменение стенда, где каноническая запись несёт объект внутри
        // объекта (records.json, v2.retype_message: email — {"value":...}).
        // Разбирается РЕКУРСИВНО тем же правилом, а не падает в UNKNOWN —
        // иначе сравнение "want" с фактически прочитанным вложенным
        // сообщением было бы структурно невозможным (разные представления
        // одного и того же значения никогда не совпали бы).
        if (n.isObject()) {
            Map<String, Value> fields = new LinkedHashMap<>();
            n.fields().forEachRemaining(e -> fields.put(e.getKey(), ofJson(e.getValue())));
            return ofRecord(fields);
        }
        // Списков схемы стенда не заводят; если такое придёт, категорию
        // назвать нечем — и это честнее, чем угадать.
        return new Value(Cat.UNKNOWN, n);
    }

    private static Value fromBigDecimal(BigDecimal bd) {
        try {
            // Целым считается число без дробной части, помещающееся в
            // знаковое 64-битное: полей другой ширины у стенда нет.
            return new Value(Cat.INT, bd.longValueExact());
        } catch (ArithmeticException e) {
            return new Value(Cat.UNKNOWN, bd);
        }
    }

    /** Нулевое значение категории — то, чем заполняется обязательное поле без умолчания. */
    public static Value zeroOf(Cat cat) {
        return switch (cat) {
            case INT -> of(0L);
            case STRING -> of("");
            case BOOL -> of(false);
            // Для «пусто» нулевым значением является оно само; неизвестная
            // категория (и вложенная запись — своего «пустого» варианта
            // у неё тоже нет) нулевого значения не имеет, и подставлять
            // что-то вместо него значило бы придумать данные.
            case NULL, UNKNOWN, RECORD -> NULL;
        };
    }

    @Override
    public boolean equals(Object o) {
        if (!(o instanceof Value other)) return false;
        if (cat != other.cat) return false;
        return Objects.equals(raw, other.raw);
    }

    @Override
    public int hashCode() { return Objects.hash(cat, raw); }

    @Override
    public String toString() { return cat + "(" + raw + ")"; }
}
