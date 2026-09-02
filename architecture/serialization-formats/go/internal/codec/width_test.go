package codec

import (
	"errors"
	"testing"
)

// Круг правок 5, находка 2. Живая проба, где схема писателя и читателя
// ОДНА И ТА ЖЕ, давала "прочиталось, но не то": значение 3000000000 не
// помещается в объявленный схемой int32, а наше кодирование усекало его
// голым приведением. Клетка, где схема не менялась, показывала неверное
// чтение — и в статье это досталось бы Protobuf.
//
// Правило одно на обе нотации и на всю пробу: запись, не помещающаяся в
// объявленный схемой тип, — расхождение стенда с его же схемой, и
// обнаруживается оно ДО кодирования, проверкой соответствия записи
// схеме писателя (тем же местом, что ловит несовпадение категории).
func TestExpectedRecordRejectsIntegerTooWideForDeclaredField(t *testing.T) {
	tooWide := int64(3000000000) // не помещается в int32, помещается в int64

	cases := []struct {
		name   string
		writer string
		field  string
	}{
		{"protobuf/int32", "user_v2_reuse_tag.desc", "login_count"},
		{"avro/int", "user_v2_add_nodefault.avsc", "age"},
	}
	for _, c := range cases {
		w := schemaPath(t, c.writer)
		rec := map[string]any{"id": int64(1), "name": "Анна", "email": "a@b", "age": int64(30),
			"login_count": int64(7)}
		// оставляем только те поля, что объявлены схемой писателя
		rec[c.field] = tooWide
		_, err := ExpectedRecord(rec, w, w)
		if err == nil {
			t.Fatalf("%s: ожидали отказ — %d не помещается в объявленный тип поля %s", c.name, tooWide, c.field)
		}
		if errors.Is(err, ErrProbeFailure) {
			t.Fatalf("%s: несоответствие записи схеме — жёсткий отказ пробы, а не исход строки: %v", c.name, err)
		}
	}
}

// Значение, которое В диапазон помещается, обязано проходить: правило
// про разрядность не должно задеть штатные записи.
func TestExpectedRecordAcceptsIntegerFittingDeclaredField(t *testing.T) {
	w := schemaPath(t, "user_v2_reuse_tag.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "login_count": int64(2147483647)}
	if _, err := ExpectedRecord(rec, w, w); err != nil {
		t.Fatalf("граничное значение int32 отвергнуто: %v", err)
	}
}

// Симметрия с Avro: кодирование Protobuf тоже обязано отказываться, а не
// усекать молча. Это вторая линия — до неё в норме не доходит, потому
// что проверка выше отсекает такую запись раньше; но именно голое
// приведение здесь и было источником находки.
func TestProtobufEncodeRejectsValueTooLargeForDeclaredInt32(t *testing.T) {
	c := newProtobufCodec()
	_, err := c.Encode(map[string]any{"id": int64(1), "name": "Анна", "login_count": int64(3000000000)},
		schemaPath(t, "user_v2_reuse_tag.desc"))
	if err == nil {
		t.Fatal("ожидали ошибку: 3000000000 не помещается в объявленный схемой int32")
	}
	if !errors.Is(err, ErrProbeFailure) {
		t.Fatalf("ошибка %v не помечена как сбой пробы", err)
	}
}

// Дробное число под целым полем — то же несоответствие записи схеме, и
// оно тоже обязано быть жёстким отказом, а не тихим округлением.
func TestExpectedRecordRejectsFractionalValueForIntegerField(t *testing.T) {
	w := schemaPath(t, "user_v1.avsc")
	if _, err := ExpectedRecord(map[string]any{"id": 1.5, "name": "Анна", "email": "a@b"}, w, w); err == nil {
		t.Fatal("ожидали отказ: 1.5 не целое, а поле объявлено целым")
	}
}

// Объявленное ПУСТОЕ умолчание даёт присутствующее поле со значением
// «пусто». Это то место, которое, по оценке ревью, переворачивает
// клетку: из «умолчание есть» и «тип — целое» одинаково выводятся и
// «положить пусто», и «положить ноль», и «не класть поле вовсе».
func TestExpectedRecordNullDefaultYieldsPresentEmptyValue(t *testing.T) {
	w := schemaPath(t, "user_v1.avsc")
	r := schemaPath(t, "user_v2_add_default.avsc")
	got, err := ExpectedRecord(map[string]any{"id": int64(1), "name": "Анна", "email": "a@b"}, w, r)
	if err != nil {
		t.Fatalf("ExpectedRecord: %v", err)
	}
	v, present := got["age"]
	if !present {
		t.Fatal("поле age отсутствует — объявленное пустое умолчание обязано дать ПРИСУТСТВУЮЩЕЕ поле")
	}
	if v != nil {
		t.Fatalf("age = %#v, ждали пусто (не ноль): умолчание объявлено пустым", v)
	}
}

func TestAvroEncodeStillRejectsTooWideValue(t *testing.T) {
	schema := Schema{
		Name:     "узкое-поле.avsc",
		Notation: NotationAvro,
		Bytes:    []byte(`{"type":"record","name":"User","fields":[{"name":"age","type":"int"}]}`),
	}
	if _, err := newAvroCodec().Encode(map[string]any{"age": int64(1) << 40}, schema); err == nil {
		t.Fatal("ожидали ошибку кодирования")
	}
}
