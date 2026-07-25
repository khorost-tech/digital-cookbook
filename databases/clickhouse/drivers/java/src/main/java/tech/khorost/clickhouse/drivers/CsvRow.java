package tech.khorost.clickhouse.drivers;

import java.time.LocalDateTime;
import java.time.format.DateTimeFormatter;

/**
 * Типизированная строка общего датасета (../../../../dataset/main.go), тот
 * же паттерн, что ../../../../../java/mergetree/.../CsvRow.java. Поля
 * датасета не содержат запятых/кавычек (event_type, country, url — из
 * фиксированных наборов без спецсимволов), поэтому парсинг простым
 * split(",") безопасен и не требует полноценного CSV-парсера.
 */
record CsvRow(
        LocalDateTime eventTime,
        long userId,
        String eventType,
        String url,
        int durationMs,
        String country,
        String revenue) {

    private static final DateTimeFormatter TIME_FORMAT = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss");

    static CsvRow parse(String line) {
        String[] f = line.split(",", -1);
        if (f.length != 7) {
            throw new IllegalArgumentException("expected 7 columns, got " + f.length + ": " + line);
        }
        return new CsvRow(
                LocalDateTime.parse(f[0], TIME_FORMAT),
                Long.parseLong(f[1]),
                f[2],
                f[3],
                Integer.parseInt(f[4]),
                f[5],
                f[6]);
    }
}
