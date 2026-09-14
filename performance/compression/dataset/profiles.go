package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
)

// EventsSmall — мелкие события одного шаблона: то, что реально течёт через
// очередь или лог. Отличаются только значениями полей, структура общая —
// ровно тот случай, для которого существует словарное сжатие.
func EventsSmall(ps []Product, n int) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		p := ps[i%len(ps)]
		e := map[string]any{
			"event":      "product_viewed",
			"product_id": p.ID,
			"sku":        p.SKU,
			"category":   p.Attrs["category"],
			"ts":         p.UpdatedAt.Unix(),
		}
		b, err := json.Marshal(e)
		if err != nil {
			panic(err)
		}
		out = append(out, b)
	}
	return out
}

// TableRow — построчная раскладка: значения одной записи лежат рядом.
func TableRow(ps []Product) []byte {
	var buf bytes.Buffer
	for _, p := range ps {
		fmt.Fprintf(&buf, "%d\t%s\t%s\t%d\t%s\t%s\n",
			p.ID, p.SKU, p.Title, p.PriceCents,
			p.Attrs["category"], p.Attrs["color"])
	}
	return buf.Bytes()
}

// TableCol — колоночная раскладка тех же значений: рядом лежат значения
// одного поля разных записей. Данные те же, отличается только порядок —
// изолирует эффект раскладки от эффекта алгоритма.
func TableCol(ps []Product) []byte {
	var buf bytes.Buffer
	for _, p := range ps {
		buf.WriteString(strconv.FormatInt(p.ID, 10))
		buf.WriteByte('\n')
	}
	for _, p := range ps {
		buf.WriteString(p.SKU)
		buf.WriteByte('\n')
	}
	for _, p := range ps {
		buf.WriteString(p.Title)
		buf.WriteByte('\n')
	}
	for _, p := range ps {
		buf.WriteString(strconv.FormatInt(p.PriceCents, 10))
		buf.WriteByte('\n')
	}
	for _, p := range ps {
		buf.WriteString(p.Attrs["category"])
		buf.WriteByte('\n')
	}
	for _, p := range ps {
		buf.WriteString(p.Attrs["color"])
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// Incompressible — псевдослучайные байты: верхняя граница того, что бывает с
// уже сжатыми данными (JPEG, .zst, зашифрованное). Настоящий JPEG жмётся чуть
// лучше случайного шума за счёт заголовков и структуры контейнера, поэтому
// профиль назван по свойству, а не по формату.
func Incompressible(n int) []byte {
	r := rand.New(rand.NewSource(7))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return b
}
