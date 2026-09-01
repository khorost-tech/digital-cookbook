// Что encoding/json принимает и что отвергает В ТЕКУЩЕМ РЕЖИМЕ СБОРКИ — и, для
// сравнения, что тот же набор случаев даёт под explicit encoding/json/v2.
//
// Вывод машиночитаемый (по строке на случай, колонки фиксированной ширины),
// чтобы режимы сравнивались обычным diff, а не глазами. Этот файл
// импортирует ТОЛЬКО encoding/json — если сюда добавить encoding/json/v2,
// демо перестанет собираться под GOEXPERIMENT=nojsonv2 (пакет v2 исключается
// build-ограничениями целиком). Колонка v2 API вынесена в отдельные файлы с
// build-тегами: v2_strict.go (goexperiment.jsonv2) и v2_absent.go (откат) —
// см. их комментарии.
package main

import (
	stdjson "encoding/json"
	"fmt"

	"tech.khorost/json-v2-cookbook/buildmode"
)

type target struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

var cases = []struct {
	Name string
	Data string
}{
	{"duplicate-keys", `{"name":"a","name":"b"}`},
	{"unknown-field", `{"name":"a","age":1,"extra":true}`},
	{"case-insensitive-match", `{"NAME":"a"}`},
	{"invalid-utf8", "{\"name\":\"\xff\xfe\"}"},
	{"trailing-data", `{"name":"a"} {"name":"b"}`},
	{"null-into-int", `{"name":"a","age":null}`},
}

func main() {
	fmt.Printf("# режим: %s (GOEXPERIMENT=%q)\n", buildmode.Current(), buildmode.Raw())
	fmt.Printf("# v2 API доступен в этой сборке: %v\n", v2Available)
	// Колонки: <случай> <encoding/json> <encoding/json/v2 либо n/a>.
	for _, c := range cases {
		var v target
		verdict := "accepted"
		if err := stdjson.Unmarshal([]byte(c.Data), &v); err != nil {
			verdict = "rejected"
		}
		fmt.Printf("%-24s %-10s %-10s\n", c.Name, verdict, v2Verdict(c.Data))
	}
}
