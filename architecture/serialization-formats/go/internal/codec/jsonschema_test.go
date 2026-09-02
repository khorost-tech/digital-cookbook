package codec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// schemaPath отдаёт схему стенда УЖЕ ПРОЧИТАННОЙ (круг правок 6:
// кодеки принимают содержимое, а не путь). Нотация выводится из
// расширения — в тестах это допустимо: в проде её сообщает манифест, а
// здесь мы сами и есть тот, кто выбирает файл.
func schemaPath(t *testing.T, name string) Schema {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", name))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("чтение схемы %s: %v", name, err)
	}
	return Schema{Name: name, Notation: notationOf(t, name), Bytes: raw}
}

func notationOf(t *testing.T, name string) string {
	t.Helper()
	switch filepath.Ext(name) {
	case ".avsc":
		return NotationAvro
	case ".desc":
		return NotationProtobuf
	case ".json":
		return NotationJSONSchema
	default:
		t.Fatalf("не знаю нотации файла %q", name)
		return ""
	}
}

// Совместимая запись проходит и кодирование, и декодирование, а байты
// СОВПАДАЮТ с контрольным json-плечом побайтово — утверждение статьи
// именно такое ("те же байты"), а не "тот же размер": две разные
// строки одинаковой длины прошли бы старую версию теста, но не
// подтвердили бы тезис (I1 ревью).
func TestJSONSchemaCodecOKMatchesJSONBytesExactly(t *testing.T) {
	c, err := New("json-schema")
	if err != nil {
		t.Fatalf("New(json-schema): %v", err)
	}
	plain, err := New("json")
	if err != nil {
		t.Fatalf("New(json): %v", err)
	}
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	v1 := schemaPath(t, "user_v1.json")

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode(json-schema): %v", err)
	}
	plainB, err := plain.Encode(rec, v1)
	if err != nil {
		// Раньше ошибка контрольного плеча глушилась "_" — тест мог
		// зеленеть, сравнивая длину закодированной записи с длиной nil.
		t.Fatalf("Encode(json) контрольного плеча: %v", err)
	}
	if !bytes.Equal(b, plainB) {
		t.Fatalf("json-schema дал %q, json — %q: плечи обязаны совпадать побайтово", b, plainB)
	}

	got, err := c.Decode(b, v1, v1)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got["email"] != "anna@example.com" {
		t.Fatalf("got %#v", got)
	}
}

// Запись, не проходящая схему писателя (лишнее поле при
// additionalProperties:false), обязана быть отвергнута ДО кодирования.
func TestJSONSchemaCodecEncodeRefusesInvalidWriterRecord(t *testing.T) {
	c, err := New("json-schema")
	if err != nil {
		t.Fatalf("New(json-schema): %v", err)
	}
	v1 := schemaPath(t, "user_v1.json")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "extra": "лишнее"}

	if _, err := c.Encode(rec, v1); err == nil {
		t.Fatal("ожидали отказ на записи с лишним полем, получили nil-ошибку")
	}
}

// remove: писатель v1 (email есть), читатель v2 (email убран). Лишнее
// поле у писателя читателю не мешает — additionalProperties читателя
// его просто не видит на входе валидации, потому что мы validируем
// декодированную запись ПОСЛЕ Unmarshal, а не сырые байты писателя.
func TestJSONSchemaCodecReaderIgnoresRemovedField(t *testing.T) {
	c, err := New("json-schema")
	if err != nil {
		t.Fatalf("New(json-schema): %v", err)
	}
	v1 := schemaPath(t, "user_v1.json")
	v2remove := schemaPath(t, "user_v2_remove.json")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := c.Decode(b, v1, v2remove); err == nil {
		t.Fatal("additionalProperties:false у читателя v2_remove обязан отвергнуть лишнее поле email")
	}
}

// Круг правок 3 ловил здесь живой баг: компилятору отдавали ПУТЬ, он
// строил из него адрес сам и принимал разделитель диска за схему URL —
// вся колонка json-schema превращалась в отказ формата, которого формат
// не делал.
//
// Круг правок 6 закрыл это по построению: схема приходит содержимым, а
// имя служит только адресом ресурса и никуда не ведёт. Тест сторожит
// именно это — имя нарочно выглядит как путь с прямыми слэшами, и
// компиляция обязана пройти, потому что файловая система в чтении схемы
// больше не участвует.
func TestJSONSchemaCodecCompilesFromContentRegardlessOfName(t *testing.T) {
	real := schemaPath(t, "user_v1.json")
	looksLikePath := Schema{
		Name:     "G:/что-то/schemas/user_v1.json",
		Notation: NotationJSONSchema,
		Bytes:    real.Bytes,
	}
	c := newJSONSchemaCodec()
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if _, err := c.Encode(rec, looksLikePath); err != nil {
		t.Fatalf("схема не скомпилировалась: %v", err)
	}
}
