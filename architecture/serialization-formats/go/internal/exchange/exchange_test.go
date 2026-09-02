package exchange

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Падающий тест (Задача 8, шаг 1): пока пакета exchange нет вовсе, эти
// проверки не компилируются. После реализации они защищают ровно три
// ловушки, названные решением контроллера: перепутанные координаты,
// испорченное/недописанное содержимое и след прошлого прогона —
// каждый файл обмена обязан нести хеш и координаты, а приём — проверять,
// что взял именно то, что ожидал.

func TestWriteThenReadCrossRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01}

	if err := WriteCross(dir, "go", "avro", "rename", "newer_reader", 2, want); err != nil {
		t.Fatalf("WriteCross: %v", err)
	}
	got, err := ReadCross(dir, "go", "avro", "rename", "newer_reader", 2)
	if err != nil {
		t.Fatalf("ReadCross: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("байты разошлись: записали %x, прочитали %x", want, got)
	}
}

func TestReadCrossMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadCross(dir, "go", "avro", "rename", "newer_reader", 0); err == nil {
		t.Fatal("ожидалась ошибка: файла обмена нет вовсе")
	}
}

// TestReadCrossCoordinateMismatch — «перепутанное имя»: файл лежит по
// имени одних координат, а внутри записаны другие. Хеш при этом честный
// (посчитан по байтам, которые реально записаны), поэтому только сверка
// координат ловит подмену.
func TestReadCrossCoordinateMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCross(dir, "go", "avro", "rename", "newer_reader", 0, []byte("payload")); err != nil {
		t.Fatalf("WriteCross: %v", err)
	}
	// Портим содержимое НЕ трогая имя файла: подменяем change внутри
	// конверта на другое значение, оставляя байты и их хеш согласованными
	// друг с другом, но не с ожидаемыми координатами чтения.
	path := CrossFileName(dir, "go", "avro", "rename", "newer_reader", 0)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение файла обмена: %v", err)
	}
	tampered := strings.Replace(string(raw), `"change":"rename"`, `"change":"remove"`, 1)
	if tampered == string(raw) {
		t.Fatal("подмена не сработала — формат конверта изменился, обнови тест")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatalf("запись подменённого файла: %v", err)
	}

	if _, err := ReadCross(dir, "go", "avro", "rename", "newer_reader", 0); err == nil {
		t.Fatal("ожидался отказ: координаты внутри конверта не совпадают с запрошенными")
	}
}

// TestReadCrossHashMismatch — «недописанный файл» / остаток порчи:
// содержимое не совпадает с зафиксированным при записи дайджестом.
func TestReadCrossHashMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCross(dir, "java", "protobuf", "unknown_field", "newer_writer", 4, []byte("hello world")); err != nil {
		t.Fatalf("WriteCross: %v", err)
	}
	path := CrossFileName(dir, "java", "protobuf", "unknown_field", "newer_writer", 4)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение файла обмена: %v", err)
	}
	// Подменяем полезную нагрузку (base64), не трогая записанный дайджест —
	// имитация обрыва записи или порчи посередине.
	tampered := strings.Replace(string(raw), `"bytes_b64":"`, `"bytes_b64":"AA`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatalf("запись подменённого файла: %v", err)
	}

	if _, err := ReadCross(dir, "java", "protobuf", "unknown_field", "newer_writer", 4); err == nil {
		t.Fatal("ожидался отказ: дайджест не совпадает с содержимым")
	}
}

// TestWriteCrossLeavesNoTempFile — запись атомарна: временный файл не
// должен остаться в каталоге обмена после успешной записи.
func TestWriteCrossLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCross(dir, "go", "json-schema", "add_default", "same", 1, []byte("x")); err != nil {
		t.Fatalf("WriteCross: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("чтение каталога: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("остался временный файл: %s", e.Name())
		}
	}
}
