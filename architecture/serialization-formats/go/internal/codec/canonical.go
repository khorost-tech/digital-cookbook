package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// CanonicalWeigher — опциональное расширение Codec: плечо умеет
// посчитать вес КАНОНИЧЕСКОЙ формы схемы, а не вес файла как он лежит
// на диске.
//
// Круг ревью 2, находка C2. Вес схемы, посчитанный по файлу как есть,
// смешивает три разных вещи под одним числом: у Avro и JSON Schema это
// текст, размеченный НАШИМИ отступами (сорок пять и пятьдесят два байта
// соответственно — не формат, а форматирование исходника), у Protobuf —
// компилированный двоичный дескриптор без единого лишнего пробела по
// устройству формата. Сравнивать эти числа напрямую значило бы сравнивать
// разное форматирование, а не разные форматы. Каноническая форма убирает
// именно эту разницу: у Avro она определена спецификацией (Parsing
// Canonical Form), у JSON Schema — решение стенда (минифицированный вид
// без декоративного title), у Protobuf канонической формы отдельно
// вводить не пришлось — дескриптор уже собирается без source_code_info
// (schemas/build-descriptors.sh, круг ревью 2, находка C1).
//
// Формат без этого расширения (json — контроль, схему не читает вовсе)
// интерфейс просто не реализует; вызывающая сторона узнаёт об этом
// через неудавшееся приведение типа и печатает 0 — содержательный ноль,
// а не потому что вес не удалось посчитать.
type CanonicalWeigher interface {
	CanonicalWeight(schema Schema) (int, error)
}

var (
	_ CanonicalWeigher = (*avroCodec)(nil)
	_ CanonicalWeigher = (*protobufCodec)(nil)
	_ CanonicalWeigher = (*jsonSchemaCodec)(nil)
)

// CanonicalWeight — длина Parsing Canonical Form схемы: она же
// возвращается String() у hamba/avro (см. доку типа в schema.go
// библиотеки — «String returns the canonical form of the schema»).
// Это официальная форма из спецификации Avro, а не решение стенда: та
// же строка используется для восьмибайтового отпечатка схемы (rabin
// fingerprint), так что за её каноничность отвечает сам формат, а не
// наш выбор.
func (c *avroCodec) CanonicalWeight(schema Schema) (int, error) {
	s, err := c.load(schema)
	if err != nil {
		return 0, err
	}
	return len(s.String()), nil
}

// CanonicalWeight — дескриптор Protobuf уже канонический: он собирается
// БЕЗ source_code_info (--exclude-source-info в build-descriptors.sh),
// а другой необязательной для чтения информации формат в дескриптор не
// кладёт. Поэтому канонический вес равен весу файла как есть — те же
// самые байты, что уже прошли сверку с манифестом.
func (c *protobufCodec) CanonicalWeight(schema Schema) (int, error) {
	return len(schema.Bytes), nil
}

// CanonicalWeight — минифицированный JSON без верхнеуровневого поля
// title. У JSON Schema нет канонической формы, определённой
// спецификацией (в отличие от Avro), поэтому это решение стенда, а не
// свойство формата: тот же набор пар ключ-значение, что и в исходном
// файле, без отступов и без title (он декоративный — человекочитаемое
// имя, не влияющее на то, что схема проверяет). "$schema" остаётся:
// без него документ нельзя интерпретировать однозначно.
func (c *jsonSchemaCodec) CanonicalWeight(schema Schema) (int, error) {
	b, err := canonicalJSONSchema(schema.Bytes)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// canonicalJSONSchema переводит документ JSON Schema в минифицированный
// вид: ТОТ ЖЕ ПОРЯДОК пар ключ-значение, что в исходном файле (важно
// для межъязыкового побайтового совпадения — Java сохраняет порядок
// вставки через LinkedHashMap-подобный ObjectNode, и обе реализации
// обязаны сохранять порядок ИЗ ИСТОЧНИКА, а не придумывать свой), без
// пробелов и переводов строк, и без верхнеуровневого "title".
//
// Обычный encoding/json.Marshal(map[string]any) здесь непригоден:
// Go-карта не хранит порядок ключей, а json.Marshal сортирует их по
// алфавиту при сериализации — на нынешней схеме результат мог бы
// случайно совпасть с исходным порядком, но это совпадение, а не
// гарантия, и она разошлась бы при первом же добавлении поля не по
// алфавиту. Поэтому здесь — потоковый разбор токенов с сохранением
// порядка, а не карта.
func canonicalJSONSchema(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeOrdered(dec)
	if err != nil {
		return nil, probeFailure("разобрать JSON Schema для канонической формы", err)
	}
	obj, ok := v.(*orderedObject)
	if !ok {
		return nil, probeFailuref("верхний уровень JSON Schema — не объект")
	}
	// title декоративен: человекочитаемое имя схемы, не влияющее на то,
	// что она проверяет. $schema не трогаем — без него документ
	// неинтерпретируем.
	obj.remove("title")
	var buf bytes.Buffer
	writeOrdered(&buf, v)
	return buf.Bytes(), nil
}

// orderedObject — JSON-объект, сохраняющий порядок вставки ключей.
// encoding/json такого типа не даёт (map[string]any теряет порядок), а
// порядок здесь — часть контракта: см. canonicalJSONSchema.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject {
	return &orderedObject{values: map[string]any{}}
}

func (o *orderedObject) set(k string, v any) {
	if _, ok := o.values[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.values[k] = v
}

func (o *orderedObject) remove(k string) {
	if _, ok := o.values[k]; !ok {
		return
	}
	delete(o.values, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// decodeOrdered читает одно JSON-значение с сохранением порядка ключей
// объектов — рекурсивно, через потоковый Decoder.Token(), а не через
// Unmarshal в map.
func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedValue(dec, tok)
}

func decodeOrderedValue(dec *json.Decoder, tok json.Token) (any, error) {
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // string, json.Number, bool, nil
	}
	switch delim {
	case '{':
		obj := newOrderedObject()
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("ключ объекта — не строка: %v", keyTok)
			}
			val, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			obj.set(key, val)
		}
		if _, err := dec.Token(); err != nil { // закрывающая '}'
			return nil, err
		}
		return obj, nil
	case '[':
		var arr []any
		for dec.More() {
			val, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil { // закрывающая ']'
			return nil, err
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("неожиданный разделитель JSON: %v", delim)
	}
}

// writeOrdered сериализует значение, полученное decodeOrdered, обратно
// в компактный JSON (без пробелов) — порядок ключей объектов берётся из
// orderedObject.keys, а не из алфавита.
func writeOrdered(buf *bytes.Buffer, v any) {
	switch t := v.(type) {
	case *orderedObject:
		buf.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			writeOrdered(buf, t.values[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeOrdered(buf, e)
		}
		buf.WriteByte(']')
	case string:
		writeJSONString(buf, t)
	case json.Number:
		buf.WriteString(t.String())
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case nil:
		buf.WriteString("null")
	}
}

func writeJSONString(buf *bytes.Buffer, s string) {
	b, _ := json.Marshal(s) // стандартное экранирование JSON-строки
	buf.Write(b)
}
