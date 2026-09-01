// Печатает фактический режим сборки и падает, если он не тот, что заказан.
// Используется скриптами перед замером: замер в неверном режиме бесполезен.
//
//	go run ./cmd/buildmode-check -want v1-old
package main

import (
	"flag"
	"fmt"
	"os"

	"tech.khorost/json-v2-cookbook/buildmode"
)

func main() {
	want := flag.String("want", "", "ожидаемый режим: v1-old или v1-on-v2")
	flag.Parse()

	got := buildmode.Current()
	fmt.Printf("GOEXPERIMENT=%q -> режим %s\n", buildmode.Raw(), got)

	if *want == "" {
		return
	}
	if string(got) != *want {
		fmt.Fprintf(os.Stderr, "ОШИБКА: заказан режим %s, собран %s — замер бессмыслен\n", *want, got)
		os.Exit(1)
	}
}
