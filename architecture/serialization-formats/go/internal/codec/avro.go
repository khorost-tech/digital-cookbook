package codec

import (
	"fmt"
	"math"
	"sync"

	"github.com/hamba/avro/v2"
)

// avroCodec — плечо Avro. Схема писателя не пишется в поток вместе с
// данными (в отличие от Object Container Format): её передаёт вызывающая
// сторона явно, как и положено для эволюции схем "по контракту", а не
// по самоописанию потока.
type avroCodec struct {
	mu    sync.Mutex
	cache map[string]avro.Schema
}

func newAvroCodec() *avroCodec {
	return &avroCodec{cache: map[string]avro.Schema{}}
}

// load разбирает схему из УЖЕ прочитанных байтов (круг правок 6:
// файл открывается один раз, при сверке с манифестом). Ключ кэша — имя
// записи манифеста, оно же тождество схемы. Неудача разбора — сбой
// пробы, а не отказ формата: настоящий Avro в этот момент ещё ничего не
// решал.
func (c *avroCodec) load(schema Schema) (avro.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.cache[schema.Name]; ok {
		return s, nil
	}
	s, err := avro.Parse(string(schema.Bytes))
	if err != nil {
		return nil, probeFailure("разобрать схему Avro", err)
	}
	c.cache[schema.Name] = s
	return s, nil
}

// PrepareSchema — см. SchemaPreparer: разобрать схему заранее, чтобы
// неудача разбора стала исходом всей клетки, а не случайной стадии.
func (c *avroCodec) PrepareSchema(schema Schema) error {
	_, err := c.load(schema)
	return err
}

func (c *avroCodec) Encode(rec map[string]any, schema Schema) ([]byte, error) {
	s, err := c.load(schema)
	if err != nil {
		return nil, fmt.Errorf("avro: схема писателя %s: %w", schema.Name, err)
	}
	rs, ok := s.(*avro.RecordSchema)
	if !ok {
		return nil, fmt.Errorf("avro: схема писателя %s: %w", schema.Name, probeFailuref("верхний уровень схемы — не record"))
	}
	// Круг правок 3: hamba/avro для generic map[string]any выбирает
	// ветку union (и в целом Int-схему) РЕФЛЕКСИЕЙ ПО GO-ТИПУ значения
	// через internal TypeResolver — int64 резолвится ТОЛЬКО в "long",
	// int32 ТОЛЬКО в "int". Normalize (см. normalize.go) намеренно
	// приводит все целые к int64 ради сравнения decode-результатов, но
	// именно поэтому его нельзя передавать в avro.Marshal как есть:
	// схема "age": ["null","int"] получает int64 и падает с "unknown
	// union type long" — это была наша ошибка, не поведение Avro.
	// avroTypedValue возвращает Go-тип к тому, что объявила КОНКРЕТНАЯ
	// схема поля, непосредственно перед кодированием.
	norm, _ := Normalize(rec).(map[string]any)
	typed := make(map[string]any, len(rs.Fields()))
	for _, f := range rs.Fields() {
		if v, ok := norm[f.Name()]; ok {
			tv, err := avroTypedValue(f.Type(), v)
			if err != nil {
				return nil, fmt.Errorf("avro: поле %s схемы %s: %w", f.Name(), schema.Name, err)
			}
			typed[f.Name()] = tv
		}
	}
	b, err := avro.Marshal(rs, typed)
	if err != nil {
		return nil, fmt.Errorf("avro: кодирование: %w", err)
	}
	return b, nil
}

// avroTypedValue приводит нормализованное значение (int64/string/bool/
// nil) к Go-типу, который ждёт КОНКРЕТНАЯ avro-схема поля: "long" —
// int64, "int" — int32, остальное — без изменений. Про union: nil
// передаётся как есть (null-ветка), а непустое значение приводится по
// первой не-null ветке — этого достаточно для всех union-полей стенда
// (везде это ["null", T]).
func avroTypedValue(schema avro.Schema, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if schema.Type() == avro.Union {
		for _, t := range schema.(*avro.UnionSchema).Types() {
			if t.Type() != avro.Null {
				return avroTypedValue(t, v)
			}
		}
		return v, nil
	}
	switch schema.Type() {
	case avro.Long:
		if i, ok := v.(int64); ok {
			return i, nil
		}
	case avro.Int:
		if i, ok := v.(int64); ok {
			// Круг правок 4, мелочь: раньше здесь стояло голое
			// int32(i), которое молча усекало. Фикстуры стенда этого
			// не задевают, но тихая порча, ПРИДУМАННАЯ нами и потом
			// списанная на Avro, — ровно та ошибка, ради поиска
			// которой стенд существует. Значение, не помещающееся в
			// объявленный схемой тип, — расхождение записи стенда с
			// его же схемой, то есть сбой пробы.
			if i < math.MinInt32 || i > math.MaxInt32 {
				return nil, probeFailuref("значение %d не помещается в объявленный схемой тип int", i)
			}
			return int32(i), nil
		}
	}
	return v, nil
}

func (c *avroCodec) Decode(b []byte, writer, reader Schema) (map[string]any, error) {
	w, err := c.load(writer)
	if err != nil {
		return nil, fmt.Errorf("avro: схема писателя %s: %w", writer.Name, err)
	}
	r, err := c.load(reader)
	if err != nil {
		return nil, fmt.Errorf("avro: схема читателя %s: %w", reader.Name, err)
	}
	// Resolve — сердце эволюции схем Avro: сам по себе readerSchema не
	// знает, какие поля писателя лишние, а какие отсутствующие поля
	// нужно заполнить default'ом читателя. Resolve возвращает ошибку
	// ровно тогда, когда настоящий Avro-читатель отказался бы читать —
	// это и есть наш "refused", а не наша самодеятельная проверка.
	resolved, err := avro.NewSchemaCompatibility().Resolve(r, w)
	if err != nil {
		return nil, fmt.Errorf("avro: схемы писателя и читателя несовместимы: %w", err)
	}
	var m map[string]any
	if err := avro.Unmarshal(resolved, b, &m); err != nil {
		return nil, fmt.Errorf("avro: декодирование: %w", err)
	}
	return Normalize(m).(map[string]any), nil
}
