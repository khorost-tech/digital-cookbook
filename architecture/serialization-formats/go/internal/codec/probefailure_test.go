package codec

import (
	"errors"
	"testing"
)

// Круг правок 4. Невозможность РАЗОБРАТЬ схему — сбой стенда, а не
// отказ формата: настоящий Avro/Protobuf/JSON Schema тут ничего не
// решал. Такие ошибки помечаются ErrProbeFailure, и классификация
// превращает их в отдельный исход, чтобы наша поломка не легла в
// таблицу как поведение формата.
//
// Круг правок 6: «прочитать» из этого списка ушло — схему кодеку
// передают уже прочитанной и сверенной, открыть файл он не может.
// Осталось «разобрать»: испорченное содержимое.
func TestUnparsableSchemaContentIsMarkedAsProbeFailure(t *testing.T) {
	garbage := []byte("это не схема ни в одной нотации")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "a@b"}

	avro := Schema{Name: "битая.avsc", Notation: NotationAvro, Bytes: garbage}
	desc := Schema{Name: "битая.desc", Notation: NotationProtobuf, Bytes: garbage}
	js := Schema{Name: "битая.json", Notation: NotationJSONSchema, Bytes: garbage}

	cases := []struct {
		name string
		run  func() error
	}{
		{"avro/encode", func() error { _, err := newAvroCodec().Encode(rec, avro); return err }},
		{"avro/decode", func() error { _, err := newAvroCodec().Decode([]byte{}, avro, avro); return err }},
		{"json-schema/encode", func() error { _, err := newJSONSchemaCodec().Encode(rec, js); return err }},
		{"json-schema/decode", func() error {
			_, err := newJSONSchemaCodec().Decode([]byte(`{}`), js, js)
			return err
		}},
		{"protobuf/encode", func() error { _, err := newProtobufCodec().Encode(rec, desc); return err }},
		{"protobuf/decode", func() error { _, err := newProtobufCodec().Decode([]byte{}, desc, desc); return err }},
		{"ожидание/avro", func() error { _, err := ExpectedRecord(rec, avro, avro); return err }},
		{"ожидание/protobuf", func() error { _, err := ExpectedRecord(rec, desc, desc); return err }},
		{"ожидание/json-schema", func() error { _, err := ExpectedRecord(rec, js, js); return err }},
	}
	for _, c := range cases {
		err := c.run()
		if err == nil {
			t.Fatalf("%s: ожидали ошибку на испорченном содержимом схемы", c.name)
		}
		if !errors.Is(err, ErrProbeFailure) {
			t.Fatalf("%s: ошибка %v не помечена ErrProbeFailure", c.name, err)
		}
	}
}

// Схема, объявленная в неизвестной нотации, — тоже сбой стенда: такую
// запись мог породить только испорченный манифест.
func TestUnknownNotationIsProbeFailure(t *testing.T) {
	s := Schema{Name: "странная.xml", Notation: "xml", Bytes: []byte("<x/>")}
	_, err := ExpectedRecord(map[string]any{"id": int64(1)}, s, s)
	if err == nil {
		t.Fatal("ожидали ошибку на неизвестной нотации")
	}
	if !errors.Is(err, ErrProbeFailure) {
		t.Fatalf("ошибка %v не помечена ErrProbeFailure", err)
	}
}

// Обратная сторона той же правки: настоящий отказ формата НЕ должен
// маскироваться под сбой пробы, иначе главная колонка таблицы опустеет.
func TestFormatRefusalsAreNotProbeFailures(t *testing.T) {
	v1 := schemaPath(t, "user_v1.avsc")
	v2 := schemaPath(t, "user_v2_add_nodefault.avsc")
	c := newAvroCodec()
	b, err := c.Encode(map[string]any{"id": int64(1), "name": "Анна", "email": "a@b"}, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	_, err = c.Decode(b, v1, v2)
	if err == nil {
		t.Fatal("ожидали отказ резолвера Avro (новое обязательное поле без default)")
	}
	if errors.Is(err, ErrProbeFailure) {
		t.Fatalf("отказ формата помечен как сбой пробы: %v", err)
	}

	js := newJSONSchemaCodec()
	_, err = js.Encode(map[string]any{"id": int64(1), "name": "Анна", "email": "a@b", "лишнее": 1},
		schemaPath(t, "user_v1.json"))
	if err == nil {
		t.Fatal("ожидали отказ JSON Schema (additionalProperties:false)")
	}
	if errors.Is(err, ErrProbeFailure) {
		t.Fatalf("отказ формата помечен как сбой пробы: %v", err)
	}
}

// Кэш разобранных схем ключуется ИМЕНЕМ ЗАПИСИ МАНИФЕСТА — тем же, что
// служит тождеством схемы. Круг правок 6: раньше ключом была строка
// пути, и один файл под двумя написаниями разбирался дважды; теперь
// путей нет вовсе, а две разные записи манифеста остаются разными
// схемами, даже если содержимое у них совпадает побайтово.
func TestSchemaCachesAreKeyedByManifestName(t *testing.T) {
	v1 := schemaPath(t, "user_v1.json")
	twin := Schema{Name: "user_v2_reuse_tag.json", Notation: v1.Notation, Bytes: v1.Bytes}

	js := newJSONSchemaCodec()
	if _, err := js.compile(v1); err != nil {
		t.Fatalf("compile(v1): %v", err)
	}
	if _, err := js.compile(v1); err != nil {
		t.Fatalf("повторный compile(v1): %v", err)
	}
	if len(js.cache) != 1 {
		t.Fatalf("json-schema: в кэше %d записей, ждали 1 — одна и та же схема", len(js.cache))
	}
	if _, err := js.compile(twin); err != nil {
		t.Fatalf("compile(twin): %v", err)
	}
	if len(js.cache) != 2 {
		t.Fatalf("json-schema: в кэше %d записей, ждали 2 — разные записи манифеста суть разные схемы", len(js.cache))
	}

	av := newAvroCodec()
	a1 := schemaPath(t, "user_v1.avsc")
	if _, err := av.load(a1); err != nil {
		t.Fatalf("avro load: %v", err)
	}
	if _, err := av.load(a1); err != nil {
		t.Fatalf("avro повторный load: %v", err)
	}
	if len(av.cache) != 1 {
		t.Fatalf("avro: в кэше %d записей, ждали 1", len(av.cache))
	}

	pb := newProtobufCodec()
	p1 := schemaPath(t, "user_v1.desc")
	if _, err := pb.load(p1); err != nil {
		t.Fatalf("protobuf load: %v", err)
	}
	if _, err := pb.load(p1); err != nil {
		t.Fatalf("protobuf повторный load: %v", err)
	}
	if len(pb.cache) != 1 {
		t.Fatalf("protobuf: в кэше %d записей, ждали 1", len(pb.cache))
	}
}

// Круг правок 4, мелочь: `case avro.Int: return int32(i)` молча усекал.
// Текущие фикстуры этого не задевают, но именно такую тихую порчу потом
// спишут на Avro, а она наша. Значение, не помещающееся в объявленный
// схемой тип, — расхождение записи стенда с его же схемой, то есть сбой
// пробы, а не находка про формат.
func TestAvroEncodeRejectsValueTooLargeForDeclaredInt(t *testing.T) {
	schema := Schema{
		Name:     "узкое-поле.avsc",
		Notation: NotationAvro,
		Bytes: []byte(`{"type":"record","name":"User","namespace":"tech.khorost.serialization",
	  "fields":[{"name":"age","type":"int"}]}`),
	}
	_, err := newAvroCodec().Encode(map[string]any{"age": int64(1) << 40}, schema)
	if err == nil {
		t.Fatal("ожидали ошибку: 2^40 не помещается в объявленный схемой int")
	}
	if !errors.Is(err, ErrProbeFailure) {
		t.Fatalf("ошибка %v не помечена ErrProbeFailure", err)
	}
}
