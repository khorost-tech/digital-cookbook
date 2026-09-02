package tech.khorost.serialization;

import java.math.BigInteger;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * Вычисление ожидания — записи, которую читатель обязан увидеть, если
 * формат отработал добросовестно.
 *
 * Считается ТОЛЬКО из записи писателя и двух схем. Результат чтения при
 * этом не «не используется», а физически ещё не существует: ожидание
 * вычисляется до первого кодирования. Это единственный способ, которым
 * исход wrong остаётся возможным, а не категорией, которую наблюдение
 * всегда обходит.
 */
final class Expect {

    private Expect() {}

    /** Найденная пара «поле читателя — поле писателя» и характер совпадения. */
    private record Match(FieldDesc writerField, boolean identity) {}

    static Map<String, Value> compute(SchemaModel writer, SchemaModel reader,
                                      Map<String, Value> record) {
        Map<String, Value> want = new LinkedHashMap<>();
        // Ожидание строится ПЕРЕБОРОМ ПОЛЕЙ ЧИТАТЕЛЯ и индексируется его
        // именами: поэтому переименование не требует отдельного шага, а
        // отброшенное поле писателя просто не участвует в переборе.
        for (FieldDesc rf : reader.fields()) {
            Match match = findWriterField(writer, reader.notation(), rf, record);

            if (match == null) {
                if (rf.hasDefault()) {
                    // Объявленное пустое умолчание даёт ПРИСУТСТВУЮЩЕЕ поле
                    // со значением «пусто» — не ноль и не пропуск поля. Из
                    // «умолчание есть» плюс «категория целое» неверно
                    // вывести «положить ноль».
                    want.put(rf.name(), rf.defaultValue());
                } else if (rf.required()) {
                    want.put(rf.name(), Value.zeroOf(rf.cat()));
                }
                // Умолчания нет и поле необязательно — в ожидании его нет вовсе.
                continue;
            }

            Value v = record.get(match.writerField().name());
            if (match.identity() && match.writerField().cat() != rf.cat()) {
                v = cast(v, rf.cat());
            }
            // Совпадение слота (номер совпал, имя нет) приведения НЕ
            // получает: это не то же поле, и приводить его категорию было
            // бы категориальной ошибкой. Ожидание получает значение
            // писателя, с категорией писателя, под именем читателя.
            want.put(rf.name(), v);
        }
        return want;
    }

    /**
     * Поля писателя просматриваются в порядке объявления; берётся первое
     * подходящее. Кандидат, для которого в записи нет значения,
     * пропускается — при записи, прошедшей проверки, такого не бывает, но
     * правило определено, чтобы две реализации не разошлись на
     * испорченных данных.
     */
    private static Match findWriterField(SchemaModel writer, String notation,
                                         FieldDesc rf, Map<String, Value> record) {
        for (FieldDesc wf : writer.fields()) {
            if (!record.containsKey(wf.name())) continue;
            switch (notation) {
                case "avro" -> {
                    // Псевдоним живёт на новой версии и указывает старое
                    // имя, поэтому смотрим псевдонимы ЧИТАТЕЛЯ.
                    if (wf.name().equals(rf.name()) || rf.aliases().contains(wf.name())) {
                        return new Match(wf, true);
                    }
                }
                case "protobuf" -> {
                    // Имена в провод не попадают — связывают номера.
                    if (wf.number() != null && wf.number().equals(rf.number())) {
                        return new Match(wf, wf.name().equals(rf.name()));
                    }
                }
                case "json-schema" -> {
                    // Механизма псевдонимов нет вовсе: переименование
                    // теряет связь целиком.
                    if (wf.name().equals(rf.name())) return new Match(wf, true);
                }
                default -> throw new Failures.SchemaSetup("неизвестная нотация: " + notation);
            }
        }
        return null;
    }

    /** Приведение с сохранением смысла. */
    static Value cast(Value v, Value.Cat target) {
        if (v.cat() == target) return v;
        if (v.cat() == Value.Cat.INT && target == Value.Cat.STRING) {
            return Value.of(Long.toString(v.asLong()));
        }
        if (v.cat() == Value.Cat.STRING && target == Value.Cat.INT) {
            Long parsed = parseDecimal(v.asString());
            // Неразбираемая строка остаётся НЕИЗМЕНЁННОЙ, а не
            // обнуляется: ноль вернёт чтение, и если бы ожидание тоже
            // стало нулём, честный wrong превратился бы в случайный ok.
            return parsed == null ? v : Value.of(parsed);
        }
        return v;
    }

    /**
     * Разбираемым считается только необязательный знак и одна или более
     * десятичных цифр — ни пробелов, ни разделителей разрядов, ни другого
     * основания, ни дробной части, ни показателя степени. Значение, не
     * помещающееся в знаковое 64-битное, разбираемым не считается.
     */
    private static Long parseDecimal(String s) {
        if (s.isEmpty()) return null;
        int i = (s.charAt(0) == '-' || s.charAt(0) == '+') ? 1 : 0;
        if (i == s.length()) return null;
        for (int k = i; k < s.length(); k++) {
            if (s.charAt(k) < '0' || s.charAt(k) > '9') return null;
        }
        try {
            BigInteger bi = new BigInteger(s);
            return bi.longValueExact();
        } catch (ArithmeticException | NumberFormatException e) {
            return null;
        }
    }

    /**
     * Проверки до вычисления. Их нарушение означает, что испорчен САМ
     * СТЕНД, а не что формат себя как-то повёл, и попадать в таблицу
     * строкой оно не должно ни в одном виде пробы.
     */
    static void checkRecordAgainstWriter(SchemaModel writer, SchemaModel reader,
                                         Map<String, Value> record, int index) {
        if (!writer.notation().equals(reader.notation())) {
            throw new Failures.ProbeRefusal("схемы писателя и читателя разной нотации: "
                    + writer.notation() + " и " + reader.notation());
        }
        for (FieldDesc wf : writer.fields()) {
            Value v = record.get(wf.name());
            if (!record.containsKey(wf.name())) {
                throw new Failures.ProbeRefusal("запись " + index + " не содержит значения для поля «"
                        + wf.name() + "» схемы писателя");
            }
            if (v.cat() == Value.Cat.NULL) {
                if (!wf.nullable()) {
                    throw new Failures.ProbeRefusal("запись " + index + ": «пусто» в поле «"
                            + wf.name() + "», где схема его не допускает");
                }
                continue;
            }
            if (wf.cat() == Value.Cat.UNKNOWN) continue;
            if (v.cat() != wf.cat()) {
                throw new Failures.ProbeRefusal("запись " + index + ": категория значения поля «"
                        + wf.name() + "» — " + v.cat() + ", схема объявляет " + wf.cat());
            }
            if (v.cat() == Value.Cat.INT && wf.bits() > 0 && !fits(v.asLong(), wf.bits())) {
                // Пока этой проверки не было, значение, не помещающееся в
                // объявленный int32, доходило до кодирования и молча
                // усекалось — и клетка, где схема писателя и читателя ОДНА
                // И ТА ЖЕ, показывала «прочиталось, но не то». Порча была
                // наша, а предъявлена была бы формату.
                throw new Failures.ProbeRefusal("запись " + index + ": значение поля «" + wf.name()
                        + "» не помещается в объявленные " + wf.bits() + " бита");
            }
        }
    }

    private static boolean fits(long v, int bits) {
        if (bits >= 64) return true;
        return v >= Integer.MIN_VALUE && v <= Integer.MAX_VALUE;
    }
}
