// Package codec — четыре плеча кодирования одной записи.
//
// Плечо json — контроль: то, с чего начинают все. json-schema кодирует
// теми же байтами и отличается только тем, что схема зарегистрирована в
// реестре и он проверяет совместимость ДО записи. Совпадение размеров
// этих двух плеч — ожидаемый результат, а не совпадение.
package codec

import "fmt"

// Codec — общий контракт для одного плеча сериализации.
//
// Круг правок 6: аргументы schema — это УЖЕ ПРОЧИТАННЫЕ и сверенные с
// манифестом схемы, а не пути к файлам. Открыть файл кодек не может, и
// это не удобство, а свойство: содержимое, по которому считали
// дайджест, и содержимое, по которому кодируют, — одни и те же байты.
type Codec interface {
	// Encode кодирует запись по схеме писателя.
	Encode(rec map[string]any, schema Schema) ([]byte, error)
	// Decode декодирует байты, записанные схемой писателя, в терминах
	// схемы читателя. Для плеча без понятия схемы (json) обе
	// игнорируются.
	Decode(b []byte, writer, reader Schema) (map[string]any, error)
}

// New создаёт кодек по имени плеча: json, json-schema, avro, protobuf.
func New(format string) (Codec, error) {
	switch format {
	case "json":
		return jsonCodec{}, nil
	case "json-schema":
		return newJSONSchemaCodec(), nil
	case "avro":
		return newAvroCodec(), nil
	case "protobuf":
		return newProtobufCodec(), nil
	default:
		return nil, fmt.Errorf("codec: неизвестный формат %q", format)
	}
}
