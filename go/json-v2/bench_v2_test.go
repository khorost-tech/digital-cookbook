// Тег сборки здесь не украшение. Под GOEXPERIMENT=nojsonv2 пакет
// encoding/json/v2 исключается build-ограничениями целиком, и один файл с
// обоими импортами не собрался бы в режиме отката, утащив за собой бенчи v1.
// То есть откат на прежний движок отнимает не только новое поведение
// encoding/json, но и сам пакет v2 — вместе с возможностью сравнить их в
// одной сборке. Поэтому BenchmarkV2* вынесены в отдельный файл с тегом,
// а BenchmarkV1* (bench_v1_test.go) остаются доступны в обоих режимах.

//go:build goexperiment.jsonv2

package jsonv2bench_test

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"tech.khorost/json-v2-cookbook/payload"
)

func BenchmarkV2Encode(b *testing.B) {
	for _, c := range payload.All() {
		b.Run(c.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := jsonv2.Marshal(c.Value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkV2Decode(b *testing.B) {
	for _, c := range payload.All() {
		b.Run(c.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var v any
				if err := jsonv2.Unmarshal(c.JSON, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
