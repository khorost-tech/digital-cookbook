package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// jsonSchemaCodec кодирует ТЕМИ ЖЕ байтами, что и jsonCodec (см. json.go)
// — единственная разница в том, что перед записью запись валидируется
// по схеме писателя, а при чтении декодированный результат валидируется
// по схеме читателя. Совпадение размеров с json-плечом — ожидаемый
// результат: см. doc.go пакета.
type jsonSchemaCodec struct {
	mu    sync.Mutex
	cache map[string]*jsonschema.Schema
}

func newJSONSchemaCodec() *jsonSchemaCodec {
	return &jsonSchemaCodec{cache: map[string]*jsonschema.Schema{}}
}

// compile собирает схему из УЖЕ прочитанных байтов.
//
// Круг правок 6: раньше компилятору отдавали путь, и он открывал файл
// сам — это и был третий-пятый повторный проход по файлу, и заодно
// источник давнего бага, когда разделители пути принимались за схему
// URL. Теперь содержимое подаётся напрямую, а имя записи манифеста
// служит и ключом кэша, и адресом ресурса — файловая система в чтении
// схемы больше не участвует вовсе.
//
// Неудача компиляции помечается как сбой пробы: схема ещё не
// заработала, формат в этот момент ничего не решал.
func (c *jsonSchemaCodec) compile(schema Schema) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sch, ok := c.cache[schema.Name]; ok {
		return sch, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.Bytes))
	if err != nil {
		return nil, probeFailure("разобрать схему JSON Schema", err)
	}
	url := schemaResourceURL(schema.Name)
	comp := jsonschema.NewCompiler()
	if err := comp.AddResource(url, doc); err != nil {
		return nil, probeFailure("зарегистрировать схему JSON Schema", err)
	}
	sch, err := comp.Compile(url)
	if err != nil {
		return nil, probeFailure("скомпилировать схему JSON Schema", err)
	}
	c.cache[schema.Name] = sch
	return sch, nil
}

// schemaResourceURL — адрес, под которым схема известна компилятору.
// Никуда не ведёт и никогда не запрашивается: ресурс зарегистрирован
// заранее, а адрес нужен лишь как имя.
func schemaResourceURL(name string) string {
	return "https://khorost.tech/serialization-formats/" + name
}

// PrepareSchema — см. SchemaPreparer.
func (c *jsonSchemaCodec) PrepareSchema(schema Schema) error {
	_, err := c.compile(schema)
	return err
}

func (c *jsonSchemaCodec) Encode(rec map[string]any, schema Schema) ([]byte, error) {
	sch, err := c.compile(schema)
	if err != nil {
		return nil, fmt.Errorf("json-schema: схема писателя %s: %w", schema.Name, err)
	}
	// Валидируем и кодируем ОДНО И ТО ЖЕ значение (круг правок 2,
	// "мелочь 1"): раньше Validate получал Normalize(rec), а
	// json.Marshal — исходный rec. Расхождение было безобидным, пока
	// нормализация не трогала ничего, что видит encoding/json, но это
	// само по себе хрупкое допущение, а не гарантия — стоит
	// нормализации начать менять представление (например, округлять
	// float), закодированные байты перестанут быть тем, что реально
	// прошло валидацию.
	norm, _ := Normalize(rec).(map[string]any)
	if err := sch.Validate(norm); err != nil {
		return nil, fmt.Errorf("json-schema: запись не проходит схему писателя: %w", err)
	}
	return json.Marshal(norm)
}

func (c *jsonSchemaCodec) Decode(b []byte, _, reader Schema) (map[string]any, error) {
	sch, err := c.compile(reader)
	if err != nil {
		return nil, fmt.Errorf("json-schema: схема читателя %s: %w", reader.Name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	norm, _ := Normalize(m).(map[string]any)
	if err := sch.Validate(norm); err != nil {
		return nil, fmt.Errorf("json-schema: запись не проходит схему читателя: %w", err)
	}
	return norm, nil
}
