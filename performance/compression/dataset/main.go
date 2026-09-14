package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	var (
		seed     = flag.Int64("seed", 42, "seed генератора")
		products = flag.Int("products", 200_000, "число товаров")
		events   = flag.Int("events", 200_000, "число мелких событий")
		out      = flag.String("out", "", "каталог для записи профилей")
	)
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "нужен -out DIR")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}

	ps := Generate(*seed, *products, 5_000, 20)

	write := func(name string, b []byte) {
		p := filepath.Join(*out, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			panic(err)
		}
		fmt.Fprintf(os.Stderr, "%s: %d байт\n", name, len(b))
	}

	write("products.ndjson", ProductsJSON(ps))
	var evBuf bytes.Buffer
	for _, e := range EventsSmall(ps, *events) {
		evBuf.Write(e)
		evBuf.WriteByte('\n')
	}
	write("events-small.ndjson", evBuf.Bytes())
	write("table-row.bin", TableRow(ps))
	write("table-col.bin", TableCol(ps))
	write("incompressible.bin", Incompressible(64*1024*1024))
}
