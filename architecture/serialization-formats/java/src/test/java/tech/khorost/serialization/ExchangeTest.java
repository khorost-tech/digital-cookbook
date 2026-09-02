package tech.khorost.serialization;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Задача 8, ось перекрёстного чтения: три ловушки обмена через файлы —
 * перепутанное имя, недописанный/испорченный файл, каждый файл несёт
 * хеш и координаты, приём проверяет, что взял именно то, что ожидал.
 * Зеркало Go-теста internal/exchange/exchange_test.go — оба обязаны
 * ловить одни и те же три случая одним и тем же способом (имя файла и
 * форма конверта совпадают побайтово между реализациями).
 */
class ExchangeTest {

    @Test
    void writeThenReadCrossRoundTrips(@TempDir Path dir) {
        byte[] want = {(byte) 0xDE, (byte) 0xAD, (byte) 0xBE, (byte) 0xEF, 0x00, 0x01};
        Exchange.writeCross(dir, "java", "avro", "rename", "newer_reader", 2, want);
        byte[] got = Exchange.readCross(dir, "java", "avro", "rename", "newer_reader", 2);
        assertArrayEquals(want, got);
    }

    @Test
    void readCrossMissingFileRefuses(@TempDir Path dir) {
        assertThrows(Failures.ProbeRefusal.class,
                () -> Exchange.readCross(dir, "java", "avro", "rename", "newer_reader", 0));
    }

    /** «Перепутанное имя»: координаты внутри файла не совпадают с запрошенными. */
    @Test
    void readCrossCoordinateMismatchRefuses(@TempDir Path dir) throws IOException {
        Exchange.writeCross(dir, "java", "avro", "rename", "newer_reader", 0, "payload".getBytes(StandardCharsets.UTF_8));
        Path path = Exchange.crossFileName(dir, "java", "avro", "rename", "newer_reader", 0);
        String raw = Files.readString(path, StandardCharsets.UTF_8);
        String tampered = raw.replace("\"change\":\"rename\"", "\"change\":\"remove\"");
        assertNotEquals(raw, tampered, "подмена не сработала — формат конверта изменился, обнови тест");
        Files.writeString(path, tampered, StandardCharsets.UTF_8);

        assertThrows(Failures.ProbeRefusal.class,
                () -> Exchange.readCross(dir, "java", "avro", "rename", "newer_reader", 0));
    }

    /** Недописанный/испорченный файл: дайджест не совпадает с содержимым. */
    @Test
    void readCrossHashMismatchRefuses(@TempDir Path dir) throws IOException {
        Exchange.writeCross(dir, "go", "protobuf", "unknown_field", "newer_writer", 4,
                "hello world".getBytes(StandardCharsets.UTF_8));
        Path path = Exchange.crossFileName(dir, "go", "protobuf", "unknown_field", "newer_writer", 4);
        String raw = Files.readString(path, StandardCharsets.UTF_8);
        String tampered = raw.replace("\"bytes_b64\":\"", "\"bytes_b64\":\"AA");
        Files.writeString(path, tampered, StandardCharsets.UTF_8);

        assertThrows(Failures.ProbeRefusal.class,
                () -> Exchange.readCross(dir, "go", "protobuf", "unknown_field", "newer_writer", 4));
    }

    @Test
    void writeCrossLeavesNoTempFile(@TempDir Path dir) throws IOException {
        Exchange.writeCross(dir, "java", "json-schema", "add_default", "same", 1,
                "x".getBytes(StandardCharsets.UTF_8));
        try (Stream<Path> entries = Files.list(dir)) {
            assertTrue(entries.noneMatch(p -> p.getFileName().toString().endsWith(".tmp")),
                    "остался временный файл");
        }
    }
}
