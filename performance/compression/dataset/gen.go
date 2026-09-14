package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"
)

type Product struct {
	ID         int64             `json:"id"`
	SKU        string            `json:"sku"`
	Title      string            `json:"title"`
	PriceCents int64             `json:"price_cents"`
	Attrs      map[string]string `json:"attrs"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// base — фиксированная точка отсчёта: без неё корпус не воспроизводим.
var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var categories = []string{"tools", "garden", "kitchen", "auto", "sport", "books"}

// Generate повторяет генератор стенда performance/inmemory дословно, включая
// порядок обращений к rand: любое отклонение сдвинет всю последовательность и
// сломает контрольную сумму корпуса.
func Generate(seed int64, products, users, viewsPerUser int) []Product {
	r := rand.New(rand.NewSource(seed))
	ps := make([]Product, 0, products)
	for i := 0; i < products; i++ {
		cat := categories[r.Intn(len(categories))]
		ps = append(ps, Product{
			ID:         int64(i + 1),
			SKU:        fmt.Sprintf("%s-%06d", cat, i+1),
			Title:      fmt.Sprintf("%s item %d", cat, i+1),
			PriceCents: int64(100 + r.Intn(999_900)),
			Attrs: map[string]string{
				"category": cat,
				"color":    []string{"red", "green", "blue", "black"}[r.Intn(4)],
				"weight_g": fmt.Sprintf("%d", 50+r.Intn(20_000)),
			},
			UpdatedAt: base.Add(time.Duration(r.Intn(180*24)) * time.Hour),
		})
	}
	return ps
}

func ProductsJSON(ps []Product) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range ps {
		if err := enc.Encode(&ps[i]); err != nil {
			panic(err)
		}
	}
	return buf.Bytes()
}
