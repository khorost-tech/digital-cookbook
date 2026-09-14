package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	var (
		scenario = flag.String("scenario", "", "matrix|zstd-impl|dictionary|columnar|incompressible")
		profiles = flag.String("profiles", "../profiles", "каталог с профилями данных")
		outDir   = flag.String("out", "", "каталог для CSV (пусто — не писать)")
		reps     = flag.Int("reps", 7, "число повторов замера")
	)
	flag.Parse()

	var (
		ms  []Measurement
		err error
	)
	switch *scenario {
	case "matrix":
		ms, err = RunMatrix(filepath.Join(*profiles, "products.ndjson"), *reps)
	case "zstd-impl":
		ms, err = RunZstdImpl(filepath.Join(*profiles, "products.ndjson"), *reps)
	case "dictionary":
		ms, err = RunDictionary(*profiles, *reps)
	case "columnar":
		ms, err = RunColumnar(*profiles, *reps)
	case "incompressible":
		ms, err = RunIncompressible(*profiles, *reps)
	default:
		fmt.Fprintf(os.Stderr, "неизвестный сценарий %q\n", *scenario)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ОШИБКА:", err)
		os.Exit(1)
	}

	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			panic(err)
		}
		writeFile := func(name string, fn func(*os.File) error) {
			f, err := os.Create(filepath.Join(*outDir, name))
			if err != nil {
				panic(err)
			}
			defer f.Close()
			if err := fn(f); err != nil {
				panic(err)
			}
			fmt.Fprintf(os.Stderr, "записан %s\n", name)
		}
		writeFile(*scenario+".csv", func(f *os.File) error { return WriteCSV(f, ms) })
		writeFile(*scenario+"-reps.csv", func(f *os.File) error { return WriteRepsCSV(f, ms) })
	}
}
