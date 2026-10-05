package bench

import (
	"strconv"
	"strings"
)

// RewritePlaceholders переводит `?` в `$N` для PostgreSQL.
// Строковых литералов с `?` в наших запросах нет, поэтому наивной замены хватает;
// если такие появятся — тест придётся расширить, а функцию усложнить.
func RewritePlaceholders(q string) string {
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
