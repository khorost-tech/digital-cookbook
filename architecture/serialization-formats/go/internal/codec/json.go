package codec

import "encoding/json"

// jsonCodec — контроль: обычный encoding/json, аргумент schema
// демонстративно игнорируется. Это плечо задаёт точку отсчёта: если
// json-schema (см. jsonschema.go) даёт другой размер байт на тех же
// данных — это баг в реализации, а не находка про формат.
type jsonCodec struct{}

func (jsonCodec) Encode(rec map[string]any, _ Schema) ([]byte, error) {
	return json.Marshal(rec)
}

func (jsonCodec) Decode(b []byte, _, _ Schema) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return Normalize(m).(map[string]any), nil
}
