package codec

import (
	"errors"
	"testing"
)

// Круг правок 4. Полнота записи проверялась только по НАБОРУ полей: все
// имена на месте — проходим дальше. Значение произвольного типа под
// правильным именем при этом проезжало и попадало прямиком в шаг
// «приведение типа», который и был рычагом трёх предыдущих кругов.
// Теперь запись обязана соответствовать схеме писателя И по типам.
func TestExpectedRecordRejectsValueOfWrongTypeForWriterSchema(t *testing.T) {
	v1 := schemaPath(t, "user_v1.avsc")
	v2 := schemaPath(t, "user_v2_retype.avsc")

	// id объявлен целым, в записи — строка.
	_, err := ExpectedRecord(map[string]any{"id": "1", "name": "Анна", "email": "a@b"}, v1, v2)
	if err == nil {
		t.Fatal("ожидали ошибку: тип значения id не совпадает с типом поля схемы писателя")
	}
	if errors.Is(err, ErrDegenerateSchema) {
		t.Fatalf("ошибка типа не должна выглядеть как вырожденная пара схем: %v", err)
	}

	// name объявлено строкой, в записи — целое.
	_, err = ExpectedRecord(map[string]any{"id": int64(1), "name": int64(5), "email": "a@b"}, v1, v2)
	if err == nil {
		t.Fatal("ожидали ошибку: тип значения name не совпадает с типом поля схемы писателя")
	}
}

// Каноническая запись стенда обязана проходить проверку типов во всех
// трёх нотациях — иначе правка выше сломала бы саму таблицу.
func TestExpectedRecordAcceptsCanonicalRecordInAllNotations(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}
	for _, ext := range []string{".avsc", ".desc", ".json"} {
		w := schemaPath(t, "user_v1"+ext)
		r := schemaPath(t, "user_v2_add_default"+ext)
		if _, err := ExpectedRecord(rec, w, r); err != nil {
			t.Fatalf("%s: каноническая запись v1 отвергнута: %v", ext, err)
		}
	}
}

// Тип поля с union ["null", T] у Avro — это T: значение обязано
// сверяться с не-null веткой, а не отвергаться как «не тот тип».
func TestExpectedRecordAcceptsUnionTypedValue(t *testing.T) {
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "a@b", "age": int64(30)}
	w := schemaPath(t, "user_v2_add_default.avsc")
	r := schemaPath(t, "user_v1.avsc")
	if _, err := ExpectedRecord(rec, w, r); err != nil {
		t.Fatalf("union-поле age отвергнуто: %v", err)
	}
}
