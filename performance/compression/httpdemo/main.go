package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		mode    = flag.String("mode", "", "server|client")
		addr    = flag.String("addr", ":8080", "адрес сервера")
		url     = flag.String("url", "http://compression-http:8080/products", "URL для клиента")
		profile = flag.String("profile", "/profiles/products.ndjson", "корпус для сервера")
		reps    = flag.Int("reps", 5, "повторов на кодировку")
		bwmbit  = flag.Float64("bwmbit", 0, "полоса в Мбит/с для клиента (0 = без ограничения); пробрасывается серверу параметром запроса и ограничивает темп записи тела ответа на уровне приложения")
	)
	flag.Parse()

	var err error
	switch *mode {
	case "server":
		err = serve(*addr, *profile)
	case "client":
		err = runClient(*url, *reps, *bwmbit)
	default:
		fmt.Fprintln(os.Stderr, "нужен -mode server|client")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ОШИБКА:", err)
		os.Exit(1)
	}
}
