// Package payload задаёт формы JSON, на которых меряются движки.
// Формы выбраны так, чтобы нагружать разные части кодека: плоскую структуру,
// длинный однородный массив, глубокую вложенность и словарь с многими ключами.
package payload

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
)

// Case — одна форма данных вместе с её JSON-представлением. JSON считается один
// раз при создании: иначе бенч декодирования мерил бы ещё и кодирование.
type Case struct {
	Name  string
	Value any
	JSON  []byte
}

type Event struct {
	ID       int64             `json:"id"`
	Kind     string            `json:"kind"`
	At       string            `json:"at"`
	Ok       bool              `json:"ok"`
	Duration float64           `json:"duration_ms"`
	Tags     map[string]string `json:"tags,omitempty"`
}

func mustJSON(name string, v any) Case {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("payload %s: %v", name, err))
	}
	return Case{Name: name, Value: v, JSON: b}
}

// newRand — фиксированный seed: одинаковые данные в каждом прогоне, иначе
// сравнивать конфигурации между собой нельзя.
func newRand() *rand.Rand { return rand.New(rand.NewPCG(42, 1027)) }

func Small() Case {
	return mustJSON("small", Event{
		ID: 1, Kind: "order.created", At: "2026-08-20T12:00:00Z",
		Ok: true, Duration: 12.5,
	})
}

func LargeArray(n int) Case {
	r := newRand()
	kinds := []string{"order.created", "order.paid", "order.shipped"}
	out := make([]Event, n)
	for i := range out {
		out[i] = Event{
			ID:       int64(i),
			Kind:     kinds[r.IntN(len(kinds))],
			At:       "2026-08-20T12:00:00Z",
			Ok:       r.IntN(10) != 0,
			Duration: r.Float64() * 100,
		}
	}
	return mustJSON("large-array", out)
}

type node struct {
	Name  string `json:"name"`
	Depth int    `json:"depth"`
	Child *node  `json:"child,omitempty"`
}

func DeepNested(depth int) Case {
	var root *node
	for i := depth; i > 0; i-- {
		root = &node{Name: fmt.Sprintf("lvl-%d", i), Depth: i, Child: root}
	}
	return mustJSON("deep-nested", root)
}

func MapHeavy(keys int) Case {
	r := newRand()
	m := make(map[string]Event, keys)
	for i := 0; i < keys; i++ {
		m[fmt.Sprintf("key-%06d", i)] = Event{
			ID: int64(i), Kind: "kv", Ok: true, Duration: r.Float64(),
		}
	}
	return mustJSON("map-heavy", m)
}

// NDJSON — вход для потокового бенча: строки, которые можно читать по одной.
func NDJSON(lines int) []byte {
	r := newRand()
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		b, err := json.Marshal(Event{
			ID: int64(i), Kind: "stream", Ok: true, Duration: r.Float64(),
		})
		if err != nil {
			panic(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// All — набор для бенчей. Размеры подобраны так, чтобы прогон занимал секунды,
// а не минуты, и при этом формы отличались по нагрузке на кодек.
func All() []Case {
	return []Case{Small(), LargeArray(1000), DeepNested(64), MapHeavy(2000)}
}
