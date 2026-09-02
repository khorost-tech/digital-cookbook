package codec_test

// Сквозная проверка: ExpectedRecord + реальный Decode + probe.Classify
// вместе обязаны воспроизвести то, что заявляет схема таблицы стенда —
// "wrong" именно там, где формат молча портит данные, и "ok" там, где
// эволюция честная. Тесты внутри internal/codec (expected_test.go)
// проверяют ExpectedRecord изолированно; этот файл — в отдельном
// пакете codec_test специально, чтобы использовать ТОЛЬКО экспортный
// API (как это будет делать cmd/probe) и тем самым исключить случай,
// когда изолированный тест зелёный, а сборка из экспортных кубиков —
// нет.

import (
	"os"
	"path/filepath"
	"testing"

	"tech.khorost/serialization-formats/internal/codec"
	"tech.khorost/serialization-formats/internal/probe"
)

// schema отдаёт схему стенда прочитанной — так её получает и cmd/probe,
// которому манифест выдаёт содержимое вместе с нотацией.
func schema(t *testing.T, name string) codec.Schema {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", name))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("чтение схемы %s: %v", name, err)
	}
	notation := map[string]string{
		".avsc": codec.NotationAvro,
		".desc": codec.NotationProtobuf,
		".json": codec.NotationJSONSchema,
	}[filepath.Ext(name)]
	if notation == "" {
		t.Fatalf("не знаю нотации файла %q", name)
	}
	return codec.Schema{Name: name, Notation: notation, Bytes: raw}
}

func classify(t *testing.T, format string, writerSchema, readerSchema codec.Schema, rec map[string]any) string {
	t.Helper()
	want, err := codec.ExpectedRecord(rec, writerSchema, readerSchema)
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	c, err := codec.New(format)
	if err != nil {
		t.Fatalf("codec.New(%s): %v", format, err)
	}
	b, encErr := c.Encode(rec, writerSchema)
	if encErr != nil {
		return probe.Classify(nil, want, encErr)
	}
	got, decErr := c.Decode(b, writerSchema, readerSchema)
	return probe.Classify(got, want, decErr)
}

// Главная находка стенда: смена типа поля у protobuf не отказывает и
// не совпадает случайно — она "wrong" именно потому, что честное
// ожидание ("1") расходится с тем, что реально возвращает decode ("").
func TestPipelineProtobufRetypeIsWrong(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got := classify(t, "protobuf", schema(t, "user_v1.desc"), schema(t, "user_v2_retype.desc"), rec)
	if got != "wrong" {
		t.Fatalf("outcome = %q, want wrong", got)
	}
}

// Вторая клетка "wrong": переиспользованный номер поля путает
// login_count с email.
func TestPipelineProtobufReuseTagIsWrong(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got := classify(t, "protobuf", schema(t, "user_v1.desc"), schema(t, "user_v2_reuse_tag.desc"), rec)
	if got != "wrong" {
		t.Fatalf("outcome = %q, want wrong", got)
	}
}

// Контрольная честная эволюция: rename по alias'у у Avro — обязан
// остаться "ok" после отмены --want, а не превратиться в "wrong"
// только из-за смены механизма вычисления ожидания.
func TestPipelineAvroRenameIsOK(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got := classify(t, "avro", schema(t, "user_v1.avsc"), schema(t, "user_v2_rename.avsc"), rec)
	if got != "ok" {
		t.Fatalf("outcome = %q, want ok", got)
	}
}

// Клетка, из-за которой отменили --want: protobuf/remove/newer_writer.
// Честное ожидание содержит default (email:""), которого в записи
// писателя v2_remove нет физически — раньше это было либо ошибкой
// инварианта, либо "wrong" без --want. Теперь — "ok", как и заявляет
// schemas/expected.json.
func TestPipelineProtobufRemoveNewerWriterIsOK(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна"}
	got := classify(t, "protobuf", schema(t, "user_v2_remove.desc"), schema(t, "user_v1.desc"), rec)
	if got != "ok" {
		t.Fatalf("outcome = %q, want ok", got)
	}
}
