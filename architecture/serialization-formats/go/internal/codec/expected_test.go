package codec

import (
	"errors"
	"reflect"
	"testing"
)

// Круг правок 2, C1: ExpectedRecord не должна физически иметь входа
// для результата decode. Раньше (--want) это была конвенция, которую
// ничего не мешало нарушить снаружи (см. отчёт, круг 1) — теперь это
// закреплено сигнатурой, а этот тест защищает саму сигнатуру от
// незаметного расширения в будущем (например, "просто добавили ещё
// один map[string]any на всякий случай").
func TestExpectedRecordSignatureHasNoDecodedResultParameter(t *testing.T) {
	ft := reflect.ValueOf(ExpectedRecord).Type()
	if ft.NumIn() != 3 {
		t.Fatalf("ExpectedRecord принимает %d параметров, ожидали ровно 3 (rec, writerSchema, readerSchema)", ft.NumIn())
	}
	if ft.In(0).Kind() != reflect.Map {
		t.Fatalf("параметр 0 — не map (запись писателя): %v", ft.In(0))
	}
	schemaType := reflect.TypeOf(Schema{})
	if ft.In(1) != schemaType || ft.In(2) != schemaType {
		t.Fatalf("параметры 1 и 2 обязаны быть схемами: %v, %v", ft.In(1), ft.In(2))
	}
	// Круг правок 6: схема — это уже прочитанное содержимое, а не путь.
	// Проверяем и это: возможность открыть файл внутри вернула бы щель
	// между сверкой дайджеста и использованием.
	if _, hasBytes := schemaType.FieldByName("Bytes"); !hasBytes {
		t.Fatal("схема обязана нести содержимое, а не указывать на него")
	}
}

// --- add_default (Avro: реальное изменение, не вырожденное) ---

func TestExpectedRecordAddDefaultNewerReader(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.avsc"), schemaPath(t, "user_v2_add_default.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": nil}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordAddDefaultNewerWriter(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": int64(30)}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_add_default.avsc"), schemaPath(t, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// Шаг 2 (отбрасывание): читатель v1 не знает про age — его не
	// должно быть в ожидании, хотя писатель его и передавал.
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// --- add_nodefault ---

func TestExpectedRecordAddNoDefaultNewerReader(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.avsc"), schemaPath(t, "user_v2_add_nodefault.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// Шаг 3, нижняя ветка: default не объявлен — нулевое значение типа.
	// (Реальный Avro на этой паре откажет ДО того, как это значение
	// вообще понадобится Classify — см. avro_test.go.)
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": int64(0)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordAddNoDefaultNewerWriter(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": int64(30)}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_add_nodefault.avsc"), schemaPath(t, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// --- remove ---

func TestExpectedRecordRemoveNewerReader(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.avsc"), schemaPath(t, "user_v2_remove.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// Это ровно та клетка, из-за которой контроллер отменил --want:
// честное ожидание для newer_writer СОДЕРЖИТ значение по умолчанию
// (пустую строку email), которого в записи писателя v2_remove
// физически нет — раньше такое ожидание отвергалось инвариантом
// "значение want обязано быть в record".
func TestExpectedRecordRemoveNewerWriter(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_remove.avsc"), schemaPath(t, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// --- rename ---

func TestExpectedRecordRenameNewerReader(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.avsc"), schemaPath(t, "user_v2_rename.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// Шаг 1: alias на читателе связывает "contact" со старым "email".
	want := map[string]any{"id": int64(1), "name": "Анна", "contact": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRenameNewerWriter(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "contact": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_rename.avsc"), schemaPath(t, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// alias объявлен на v2 (читатель НЕ v1), у v1 aliases нет — со
	// стороны читателя v1 "email" не находит пары в writer'е и
	// получает нулевое значение, а не значение "contact".
	want := map[string]any{"id": int64(1), "name": "Анна", "email": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// --- retype (шаг 4 — главный, protobuf: заявленный контроллером пример) ---

func TestExpectedRecordRetypeNewerReaderProtobuf(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.desc"), schemaPath(t, "user_v2_retype.desc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// "1" становится "1", а не "" — именно это отличает честное
	// ожидание от того, что реально возвращает Decode (см.
	// TestProtobufCodecRetypeGivesEmptyStringNotError).
	want := map[string]any{"id": "1", "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRetypeNewerWriterProtobuf(t *testing.T) {
	rec := map[string]any{"id": "1", "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_retype.desc"), schemaPath(t, "user_v1.desc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// --- unknown_field ---

func TestExpectedRecordUnknownFieldNewerReader(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.avsc"), schemaPath(t, "user_v2_unknown_field.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "nickname": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordUnknownFieldNewerWriter(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "nickname": "anya"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_unknown_field.avsc"), schemaPath(t, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// --- reuse_tag: единственное изменение, не одинаковое во всех трёх форматах ---

// protobuf: matchByNumber сталкивает СЕМАНТИЧЕСКИ РАЗНЫЕ поля (email и
// login_count) на одном номере — конвертация "с сохранением смысла"
// невозможна (email не парсится как целое), поэтому шаг 4 оставляет
// значение как есть. Это и даёт честный "wrong" вместо случайного "ok"
// при сравнении с реальным decode (получающим 0 из-за несовпадения
// wire type — см. TestProtobufCodecReuseTagGivesZeroNotError).
func TestExpectedRecordReuseTagNewerReaderProtobuf(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.desc"), schemaPath(t, "user_v2_reuse_tag.desc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "login_count": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordReuseTagNewerWriterProtobuf(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "login_count": int64(7)}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_reuse_tag.desc"), schemaPath(t, "user_v1.desc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// Круг правок 3: номер совпал (3), но ИМЯ — нет (login_count у
	// писателя, email у читателя) — это переиспользование слота, а не
	// ретайп одного и того же поля, и конвертация в string НЕ
	// применяется вовсе (иначе значение "0" в поле, переиспользованном
	// под другой смысл, случайно совпадало бы с decode — та самая дыра
	// круга 2). Значение переносится как есть, с исходным типом int64.
	want := map[string]any{"id": int64(1), "name": "Анна", "email": int64(7)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// Avro и JSON Schema: user_v2_reuse_tag.* структурно НЕ отличается от
// v1 в этих нотациях (переиспользование номера поля — понятие, которого
// у них попросту нет) — вырожденный случай, ExpectedRecord обязана его
// распознать и вернуть ErrDegenerateSchema, а не тихую "копию v1".
func TestExpectedRecordReuseTagIsDegenerateForAvro(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	_, err := ExpectedRecord(rec, schemaPath(t, "user_v1.avsc"), schemaPath(t, "user_v2_reuse_tag.avsc"))
	if !errors.Is(err, ErrDegenerateSchema) {
		t.Fatalf("ожидали ErrDegenerateSchema, получили %v", err)
	}
}

func TestExpectedRecordReuseTagIsDegenerateForJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	_, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_reuse_tag.json"))
	if !errors.Is(err, ErrDegenerateSchema) {
		t.Fatalf("ожидали ErrDegenerateSchema, получили %v", err)
	}
}

// Контрольный случай: та же самая схема дважды (направление "same") —
// это НЕ вырожденность, это законный контроль, и вырожденность не
// должна на него срабатывать (иначе базового "ok" контроля не было бы
// вовсе).
//
// Круг правок 3: прежняя версия этого теста проходила бы и у функции,
// которая была бы просто "return rec, nil" — при writerSchema ==
// readerSchema тождественная запись действительно правильный ответ ДЛЯ
// ЛЮБОЙ корректной реализации, так что сам факт совпадения ничего не
// доказывает. Усиление: в rec добавлено поле, которого схема НЕ
// объявляет вовсе — настоящая реализация строит вывод ПЕРЕБОРОМ ПОЛЕЙ
// СХЕМЫ (а не эхом record), и обязана это поле отбросить; заглушка
// "вернуть rec как есть" его бы сохранила.
func TestExpectedRecordSameSchemaIsNotDegenerate(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	v1 := schemaPath(t, "user_v1.avsc")
	got, err := ExpectedRecord(rec, v1, v1)
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("got %#v, want %#v — при одной и той же схеме ожидание совпадает с записью", got, rec)
	}
}

// Круг правок 7. Покрытие всех полей схемы проверялось, обратное — нет.
// Запись с лишним ключом доезжала до кодирования, и клетка, где схема
// писателя и читателя ОДНА И ТА ЖЕ, показывала «прочиталось, но не то»
// у контрольного плеча и отказ у json-schema. Порча наша, а предъявлена
// была бы формату — тот же класс, что разрядность.
func TestExpectedRecordRejectsKeyNotDeclaredByWriterSchema(t *testing.T) {
	rec := map[string]any{
		"id": int64(1), "name": "Анна", "email": "anna@example.com",
		"поле_не_объявленное_схемой": "лишнее",
	}
	for _, ext := range []string{".avsc", ".desc", ".json"} {
		v1 := schemaPath(t, "user_v1"+ext)
		if _, err := ExpectedRecord(rec, v1, v1); err == nil {
			t.Fatalf("%s: ожидали отказ — в записи ключ, которого нет в схеме писателя", ext)
		}
	}
}

// Решение «совпадение номера — не тождество поля» (см. matchWriterField):
// значение переносится КАК ЕСТЬ, без приведения типа. Круг правок 7
// показал, что на четырёх канонических записях из пяти это решение
// таблицей не проверяется: и с приведением, и без него исход остаётся
// wrong. Проверяется оно ровно на записи с email "0" — там приведение
// дало бы целое 0, совпадающее с тем, что возвращает чтение, и клетка
// перевернулась бы в ok.
func TestExpectedRecordDoesNotConvertOnReusedFieldNumber(t *testing.T) {
	rec := map[string]any{"id": int64(4), "name": "Глеб", "email": "0"}
	got, err := ExpectedRecord(rec,
		schemaPath(t, "user_v1.desc"), schemaPath(t, "user_v2_reuse_tag.desc"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	v, ok := got["login_count"]
	if !ok {
		t.Fatalf("в ожидании нет поля login_count: %#v", got)
	}
	if v != "0" {
		t.Fatalf("login_count = %#v (%T), ждали строку \"0\": на переиспользованном номере приведения типа быть не должно", v, v)
	}
}

// --- JSON Schema: те же семь изменений × два направления. ---
//
// Круг правок 3: раньше JSON Schema не была покрыта НИ ОДНИМ тестом,
// кроме вырожденного reuse_tag — и ложный "wrong" (регрессия того же
// круга, шаг 3 без учёта "required") завёлся ровно в этой дыре. Ниже —
// полное покрытие: matchByName без alias'ов и Required, взятый из
// "required" схемы, а не из наличия в "properties".

func TestExpectedRecordAddDefaultNewerReaderJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_add_default.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// age не входит в "required" v2_add_default.json — необязательное
	// поле без default, которого писатель не прислал, в ожидание не
	// попадает (это и есть регрессия круга 2, теперь закрытая тестом).
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordAddDefaultNewerWriterJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": int64(30)}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_add_default.json"), schemaPath(t, "user_v1.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordAddNoDefaultNewerReaderJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_add_nodefault.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// age входит в "required" v2_add_nodefault.json — обязательное поле
	// без default получает нулевое значение типа (реальная json-schema
	// на этой паре откажет раньше, чем это значение понадобится
	// Classify — required не выполняется).
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": int64(0)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordAddNoDefaultNewerWriterJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "age": int64(30)}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_add_nodefault.json"), schemaPath(t, "user_v1.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRemoveNewerReaderJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_remove.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRemoveNewerWriterJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_remove.json"), schemaPath(t, "user_v1.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// email — в "required" v1.json, у писателя v2_remove его нет —
	// нулевое значение типа.
	want := map[string]any{"id": int64(1), "name": "Анна", "email": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRenameNewerReaderJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_rename.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// У JSON Schema нет alias — "contact" не находит пару у писателя
	// (там только "email"), а "contact" в "required" v2_rename.json —
	// нулевое значение типа, а не значение email.
	want := map[string]any{"id": int64(1), "name": "Анна", "contact": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRenameNewerWriterJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "contact": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_rename.json"), schemaPath(t, "user_v1.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRetypeNewerReaderJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_retype.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// matchByName: "id" находит пару по имени и у JSON Schema —
	// приведение типа работает точно так же, как у Protobuf/Avro.
	want := map[string]any{"id": "1", "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordRetypeNewerWriterJSONSchema(t *testing.T) {
	rec := map[string]any{"id": "1", "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_retype.json"), schemaPath(t, "user_v1.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordUnknownFieldNewerReaderJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v1.json"), schemaPath(t, "user_v2_unknown_field.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	// nickname не входит в "required" v2_unknown_field.json — это и
	// есть регрессия круга 2 (было: ok, стало: wrong из-за добавленного
	// nickname:""); теперь снова ok — nickname просто не появляется.
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestExpectedRecordUnknownFieldNewerWriterJSONSchema(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "nickname": "anya"}
	got, err := ExpectedRecord(rec, schemaPath(t, "user_v2_unknown_field.json"), schemaPath(t, "user_v1.json"))
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	want := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// reuse_tag у JSON Schema вырожден в обоих направлениях (см. также
// TestExpectedRecordReuseTagIsDegenerateForJSONSchema выше, которая
// проверяет направление newer_reader) — здесь для симметрии проверено
// newer_writer.
func TestExpectedRecordReuseTagIsDegenerateForJSONSchemaNewerWriter(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	_, err := ExpectedRecord(rec, schemaPath(t, "user_v2_reuse_tag.json"), schemaPath(t, "user_v1.json"))
	if !errors.Is(err, ErrDegenerateSchema) {
		t.Fatalf("ожидали ErrDegenerateSchema, получили %v", err)
	}
}
