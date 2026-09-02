package tech.khorost.serialization;

/**
 * Три вида неудачи, которые обязаны различаться, иначе таблица врёт.
 *
 * Пока отказ формата и поломка пробы выглядели одинаково, колонка
 * формата заполнялась отказами, которых формат не делал.
 */
final class Failures {

    private Failures() {}

    /** Отказ пробы: строк не печатается вовсе, код возврата не 0. */
    static final class ProbeRefusal extends RuntimeException {
        ProbeRefusal(String message) { super(message); }
    }

    /** Сбой подготовки схемы: исход {@code error} — сломалась проба, а не формат. */
    static final class SchemaSetup extends RuntimeException {
        SchemaSetup(String message, Throwable cause) { super(message, cause); }
        SchemaSetup(String message) { super(message); }
    }

    /** Поведение формата при уже рабочей схеме: исход {@code refused}. */
    static final class FormatRefusal extends RuntimeException {
        FormatRefusal(String message) { super(message); }
        FormatRefusal(String message, Throwable cause) { super(message, cause); }
    }
}
