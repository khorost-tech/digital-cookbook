// Небольшой HTTP-сервис — реальная, но быстрая цель сборки и тестов для замеров
// эффекта кэша в разделе github-actions. Одна чистая функция (Slugify) вынесена
// отдельно и покрыта табличным тестом; внешняя зависимость (google/uuid) нужна,
// чтобы у module-кэша было что переиспользовать между сборками.
package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify превращает произвольную строку в URL-slug: нижний регистр, любые серии
// не-буквенно-цифровых символов схлопываются в один дефис, крайние дефисы срезаются.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func handler(w http.ResponseWriter, r *http.Request) {
	reqID := uuid.NewString()
	w.Header().Set("X-Request-Id", reqID)
	fmt.Fprintf(w, "%s\n", Slugify(r.URL.Query().Get("q")))
}

func main() {
	http.HandleFunc("/slug", handler)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	_ = http.ListenAndServe(":8080", nil)
}
