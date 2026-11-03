// Сжигатель для оси профилей: процесс жжёт CPU в двух функциях в пропорции
// 3 : 1 по числу итераций — эталон доли CPU 75% / 25% задаёт счёт работы.
//
//	burn self               цикл внутри burnA и burnB — они сами листовые функции
//	burn shared             burnA и burnB вызывают общую листовую функцию work
//	burn pprof-shared SEC   то же, что shared, и одновременно профиль runtime/pprof самого
//	                        процесса на SEC секунд (контроль для eBPF-профилировщика,
//	                        который снимает тот же процесс): печатает версию Go и
//	                        полное время burnA, burnB, work
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"time"

	"github.com/google/pprof/profile"
)

//go:noinline
func work(n int) uint64 {
	var x uint64 = 1
	for i := 0; i < n; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	return x
}

//go:noinline
func burnA() uint64 { return work(3_000_000) }

//go:noinline
func burnB() uint64 { return work(1_000_000) }

//go:noinline
func burnSelfA() uint64 {
	var x uint64 = 1
	for i := 0; i < 3_000_000; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	return x
}

//go:noinline
func burnSelfB() uint64 {
	var x uint64 = 1
	for i := 0; i < 1_000_000; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	return x
}

var sink uint64

func pprofShared(sec int) {
	go func() {
		for {
			sink += burnA()
			sink += burnB()
		}
	}()
	var buf bytes.Buffer
	pprof.StartCPUProfile(&buf)
	time.Sleep(time.Duration(sec) * time.Second)
	pprof.StopCPUProfile()
	p, err := profile.Parse(&buf)
	if err != nil {
		panic(err)
	}
	cum := map[string]int64{}
	var total int64
	for _, s := range p.Sample {
		v := s.Value[1] // cpu, нс
		total += v
		seen := map[string]bool{}
		for _, loc := range s.Location {
			for _, line := range loc.Line {
				if n := line.Function.Name; !seen[n] {
					seen[n] = true
					cum[n] += v
				}
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"go": runtime.Version(), "seconds": sec, "total_ns": total, "cum_ns": map[string]int64{
		"main.burnA": cum["main.burnA"], "main.burnB": cum["main.burnB"], "main.work": cum["main.work"]}})
	fmt.Printf("RESULT %s\n", b)
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == "pprof-shared" {
		sec, _ := strconv.Atoi(os.Args[2])
		pprofShared(sec)
		return
	}
	shared := len(os.Args) > 1 && os.Args[1] == "shared"
	for {
		if shared {
			sink += burnA()
			sink += burnB()
		} else {
			sink += burnSelfA()
			sink += burnSelfB()
		}
	}
}
