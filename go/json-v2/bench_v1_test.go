package jsonv2bench_test

import (
	stdjson "encoding/json"
	"testing"

	"tech.khorost/json-v2-cookbook/buildmode"
	"tech.khorost/json-v2-cookbook/payload"
)

// TestModeReported печатает режим в лог: он попадает в вывод и дальше в отчёт,
// поэтому колонку нельзя перепутать постфактум.
func TestModeReported(t *testing.T) {
	t.Logf("режим сборки: %s (GOEXPERIMENT=%q)", buildmode.Current(), buildmode.Raw())
}

func BenchmarkV1Encode(b *testing.B) {
	for _, c := range payload.All() {
		b.Run(c.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := stdjson.Marshal(c.Value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkV1Decode(b *testing.B) {
	for _, c := range payload.All() {
		b.Run(c.Name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var v any
				if err := stdjson.Unmarshal(c.JSON, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
