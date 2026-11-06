// dod-go — ядра стенда performance/data-oriented на Go.
//
//	dod-go <ядро> [ключ=значение ...]  → одна строка JSON на stdout
//
// Ядра, параметры и генератор (splitmix64) — те же, что в c/dod.c:
// контрольные суммы двух языков обязаны совпасть. reps=0 — только подготовка
// данных без прогона ядра: так perf stat отделяет ядро от setup.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/cpu"
)

var rngState uint64

func rnd() uint64 {
	rngState += 0x9E3779B97F4A7C15
	z := rngState
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func param(key string, def int64) int64 {
	for _, a := range os.Args[2:] {
		if v, ok := strings.CutPrefix(a, key+"="); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				panic(err)
			}
			return n
		}
	}
	return def
}

// innerRuns — сколько прогонов ядра в одном замере: не меньше 2^24 операций,
// чтобы короткое ядро не тонуло в разрешении часов. Та же формула — в C и в
// scripts/run-checks.py (разность счётчиков perf).
func innerRuns(ops int64) int64 {
	if ops >= 1<<24 {
		return 1
	}
	return (1<<24 + ops - 1) / ops
}

// measure — прогрев (если reps > 0) и reps замеров; медиана нс на операцию.
func measure(k func() uint64, ops int64) (float64, uint64) {
	reps, inner := param("reps", 7), innerRuns(ops)
	if reps == 0 {
		return 0, 0
	}
	check := k()
	for i := int64(1); i < inner; i++ {
		k()
	}
	t := make([]float64, reps)
	for r := range t {
		s := time.Now()
		var c uint64
		for range inner {
			c = k()
		}
		t[r] = float64(time.Since(s).Nanoseconds()) / float64(ops*inner)
		if c != check {
			fmt.Fprintln(os.Stderr, "контрольная сумма плавает")
			os.Exit(2)
		}
	}
	sort.Float64s(t)
	return t[reps/2], check
}

// particle — восемь полей по 8 байт: ровно 64 байта.
type particle struct{ f [8]float64 }

// Горячие и холодные поля той же частицы: цикл пересечения границы читает
// только x и vx, остальные шесть полей лежат отдельно и в кэш не попадают.
type hotFields struct{ x, vx float64 }
type coldFields struct{ f [6]float64 }

// aligned возвращает срез uint64 длиной n слов, начало которого выровнено на 256 байт.
func aligned(n int64) []uint64 {
	buf := make([]uint64, n+32)
	off := (256 - uintptr(unsafe.Pointer(&buf[0]))%256) % 256 / 8
	return buf[off : int64(off)+n]
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "ядро?")
		os.Exit(1)
	}
	kernel := os.Args[1]
	rngState = uint64(param("seed", 42))
	var ns float64
	var check uint64
	var ops int64

	switch kernel {
	case "chase":
		// Случайный обход по одному циклу Саттоло; узел — одно слово на 128 байт.
		ws := param("ws", 1<<20)
		nodes := ws / 128
		buf := aligned(ws / 8)
		perm := make([]uint64, nodes)
		for i := range perm {
			perm[i] = uint64(i)
		}
		for i := nodes - 1; i > 0; i-- {
			j := int64(rnd() % uint64(i))
			perm[i], perm[j] = perm[j], perm[i]
		}
		for i := int64(0); i < nodes; i++ {
			buf[i*16] = perm[i] * 16
		}
		perm = nil
		steps := param("steps", 1<<22)
		ops = steps
		ns, check = measure(func() uint64 {
			i := uint64(0)
			for s := int64(0); s < steps; s++ {
				i = buf[i]
			}
			return i
		}, ops)
	case "seq":
		ws := param("ws", 1<<20)
		buf := aligned(ws / 8)
		for i := range buf {
			buf[i] = rnd() & 0xffff
		}
		ops = int64(len(buf))
		ns, check = measure(func() uint64 {
			s := uint64(0)
			for _, v := range buf {
				s += v
			}
			return s
		}, ops)
	case "aos", "soa", "ptr":
		n := param("n", 1<<21)
		fields := param("fields", 1)
		ops = n
		// значения — целые < 1000: сумма точна в float64 при любом порядке
		val := func(i int64, j int) float64 { return float64((i*8 + int64(j)) % 1000) }
		switch kernel {
		case "aos":
			aos := make([]particle, n)
			for i := range aos {
				for j := range 8 {
					aos[i].f[j] = val(int64(i), j)
				}
			}
			ns, check = measure(func() uint64 {
				s := 0.0
				if fields == 1 {
					for i := range aos {
						s += aos[i].f[0]
					}
				} else {
					for i := range aos {
						for j := range 8 {
							s += aos[i].f[j]
						}
					}
				}
				return uint64(s)
			}, ops)
		case "soa":
			var soa [8][]float64
			for j := range 8 {
				soa[j] = make([]float64, n)
				for i := range soa[j] {
					soa[j][i] = val(int64(i), j)
				}
			}
			ns, check = measure(func() uint64 {
				s := 0.0
				if fields == 1 {
					for _, v := range soa[0] {
						s += v
					}
				} else {
					for i := range soa[0] {
						for j := range 8 {
							s += soa[j][i]
						}
					}
				}
				return uint64(s)
			}, ops)
		case "ptr":
			// Объекты выделяются по одному и подряд; shuffled=1 перемешивает
			// порядок обхода — как срез указателей после сортировки или вставок.
			ptrs := make([]*particle, n)
			for i := range ptrs {
				p := new(particle)
				for j := range 8 {
					p.f[j] = val(int64(i), j)
				}
				ptrs[i] = p
			}
			if param("shuffled", 0) == 1 {
				for i := n - 1; i > 0; i-- {
					j := int64(rnd() % uint64(i+1))
					ptrs[i], ptrs[j] = ptrs[j], ptrs[i]
				}
			}
			ns, check = measure(func() uint64 {
				s := 0.0
				if fields == 1 {
					for _, p := range ptrs {
						s += p.f[0]
					}
				} else {
					for _, p := range ptrs {
						for j := range 8 {
							s += p.f[j]
						}
					}
				}
				return uint64(s)
			}, ops)
		}
	case "cross":
		// Сколько частиц пересечёт границу за шаг: x + vx >= 1000.
		n := param("n", 1<<21)
		ops = n
		if param("split", 0) == 1 {
			hot := make([]hotFields, n)
			cold := make([]coldFields, n)
			for i := range hot {
				hot[i] = hotFields{float64((i * 8) % 1000), float64((i*8 + 1) % 1000)}
				for j := range 6 {
					cold[i].f[j] = float64((i*8 + j + 2) % 1000)
				}
			}
			ns, check = measure(func() uint64 {
				c := uint64(0)
				for i := range hot {
					if hot[i].x+hot[i].vx >= 1000 {
						c++
					}
				}
				return c
			}, ops)
		} else {
			aos := make([]particle, n)
			for i := range aos {
				for j := range 8 {
					aos[i].f[j] = float64((i*8 + j) % 1000)
				}
			}
			ns, check = measure(func() uint64 {
				c := uint64(0)
				for i := range aos {
					if aos[i].f[0]+aos[i].f[1] >= 1000 {
						c++
					}
				}
				return c
			}, ops)
		}
	case "fs":
		// Две горутины, атомарный инкремент; второй счётчик — через dist байт
		// от первого, база выровнена на 256.
		dist := param("dist", 8)
		base := aligned(128)
		a := &base[0]
		b := &base[dist/8]
		iters := param("iters", 20_000_000)
		ops = iters
		worker := func(c *uint64, wg *sync.WaitGroup) {
			defer wg.Done()
			for range iters {
				atomic.AddUint64(c, 1)
			}
		}
		ns, check = measure(func() uint64 {
			*a, *b = 0, 0
			var wg sync.WaitGroup
			wg.Add(2)
			go worker(a, &wg)
			go worker(b, &wg)
			wg.Wait()
			return *a + *b
		}, ops)
	case "warmup": // разгон частоты перед замерами
		end := time.Now().Add(2 * time.Second)
		x := uint64(0)
		for time.Now().Before(end) {
			for i := range uint64(1_000_000) {
				x += i
			}
		}
		check = x
	case "branch", "branchless", "filter", "filter_branchless":
		n := param("n", 1<<22)
		data := make([]uint8, n)
		for i := range data {
			data[i] = uint8(rnd() & 0xff)
		}
		if param("sorted", 0) == 1 { // сортировка подсчётом
			var cnt [256]int64
			for _, v := range data {
				cnt[v]++
			}
			p := 0
			for v := range 256 {
				for range cnt[v] {
					data[p] = uint8(v)
					p++
				}
			}
		}
		ops = n
		out := make([]uint8, n+1)
		switch kernel {
		case "filter":
			// Отбор байтов >= 128 в выходной срез: запись по условному
			// индексу компилятор в cmov не превращает — ветка остаётся.
			ns, check = measure(func() uint64 {
				k := 0
				for _, v := range data {
					if v >= 128 {
						out[k] = v
						k++
					}
				}
				return uint64(k) + uint64(out[k/2])
			}, ops)
		case "filter_branchless":
			ns, check = measure(func() uint64 {
				k := 0
				for _, v := range data {
					out[k] = v                   // пишем всегда,
					k += int((uint(v) >> 7) & 1) // сдвигаемся только на подходящем
				}
				return uint64(k) + uint64(out[k/2])
			}, ops)
		case "branch":
			ns, check = measure(func() uint64 {
				s := uint64(0)
				for _, v := range data {
					if v >= 128 {
						s += uint64(v)
					}
				}
				return s
			}, ops)
		case "branchless":
			ns, check = measure(func() uint64 {
				s := uint64(0)
				for _, v := range data {
					x := int64(v)
					mask := ^((x - 128) >> 63) // все единицы, если v >= 128
					s += uint64(x & mask)
				}
				return s
			}, ops)
		}
	case "pad":
		// Размер паддинга от ложного разделения, который x/sys/cpu берёт для этой архитектуры.
		fmt.Printf("{\"lang\":\"go\",\"kernel\":\"pad\",\"goarch\":%q,\"cache_line_pad\":%d}\n",
			runtime.GOARCH, unsafe.Sizeof(cpu.CacheLinePad{}))
		return
	default:
		fmt.Fprintln(os.Stderr, "неизвестное ядро", kernel)
		os.Exit(1)
	}

	out, _ := json.Marshal(map[string]any{
		"lang": "go", "kernel": kernel, "args": strings.Join(os.Args[2:], " "),
		"ns_per_op": float64(int64(ns*10000)) / 10000, "ops": ops, "check": check,
	})
	fmt.Println(string(out))
}
