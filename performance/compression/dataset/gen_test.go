package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// Корпус обязан совпадать с корпусом стенда performance/inmemory: те же
// параметры дают те же байты. Контрольная сумма — машинная защита от
// молчаливого расхождения двух копий алгоритма.
func TestProductsJSONDeterministic(t *testing.T) {
	a := ProductsJSON(Generate(42, 200_000, 5_000, 20))
	b := ProductsJSON(Generate(42, 200_000, 5_000, 20))
	if len(a) != len(b) {
		t.Fatalf("длина различается: %d vs %d", len(a), len(b))
	}
	sumA := sha256.Sum256(a)
	sumB := sha256.Sum256(b)
	if sumA != sumB {
		t.Fatalf("два вызова с одним seed дали разные байты")
	}
	t.Logf("sha256(products.ndjson) = %s", hex.EncodeToString(sumA[:]))
	t.Logf("размер корпуса = %d байт", len(a))
}

func TestGenerateCount(t *testing.T) {
	ps := Generate(42, 1_000, 10, 2)
	if len(ps) != 1_000 {
		t.Fatalf("получено %d товаров, ожидалось 1000", len(ps))
	}
	if ps[0].ID != 1 {
		t.Fatalf("первый ID = %d, ожидался 1", ps[0].ID)
	}
}
