package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	exp := flag.String("experiment", "all", "bloom|hll|countmin|cuckoo|throughput|all")
	flag.Parse()

	run := map[string]func(){
		"bloom":      runBloom,
		"hll":        runHLL,
		"countmin":   runCountMin,
		"cuckoo":     runCuckoo,
		"throughput": runThroughput,
	}
	order := []string{"bloom", "hll", "countmin", "cuckoo", "throughput"}

	if *exp == "all" {
		for _, k := range order {
			fmt.Printf("=== %s ===\n", k)
			run[k]()
		}
		return
	}
	fn, ok := run[*exp]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown experiment %q\n", *exp)
		os.Exit(2)
	}
	fn()
}
