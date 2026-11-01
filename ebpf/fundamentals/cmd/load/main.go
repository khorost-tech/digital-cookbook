// Нагрузка — эталон стенда: делает ровно столько событий, сколько сказано, и
// печатает свой счёт. eBPF-программы сверяются с ним, а не с собой.
//
// Нагрузка ждёт строку на stdin перед работой: загрузчик успевает узнать её PID
// и прицепить программы, и ни одно событие не происходит до этого.
//
//	load open N        N раз открыть и закрыть файл (openat)
//	load call N        N раз вызвать Go-функцию main.targetFunc (для uprobe)
//	load callgrow N    N раз вызвать main.targetGrow в новой горутине — пролог растит стек
//	load burst N K     N вызовов getppid в K потоках, поток i закреплён за CPU i % NumCPU
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

const target = "/tmp/ebpf-load-target"

//go:noinline
func targetFunc(i int) int { return i*2 + 1 }

// targetGrow — функция с кадром 12 КБ, больше начального стека горутины (2 КБ, stackMin): её
// пролог при каждом вызове в новой горутине уходит в morestack, рантайм Go
// копирует стек в новое место и выполняет функцию с первой инструкции заново.
// Нагрузка запускается с GODEBUG=adaptivestackstart=0, чтобы начальный стек
// не подстраивался под прошлые горутины.
//
//go:noinline
func targetGrow(i int) int {
	var pad [12288]byte
	pad[i%len(pad)] = byte(i)
	return int(pad[(i*7)%len(pad)]) + i
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "load open N | call N | burst N K")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[2])
	if os.Args[1] == "open" { // файл создаётся до старта: его openat не входит в счёт
		if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
			panic(err)
		}
	}
	fmt.Printf("READY %d\n", os.Getpid())
	bufio.NewReader(os.Stdin).ReadString('\n')

	out := map[string]any{"mode": os.Args[1], "n": n, "pid": os.Getpid()}
	switch os.Args[1] {
	case "open":
		done := 0
		for i := 0; i < n; i++ {
			f, err := os.Open(target)
			if err != nil {
				panic(err)
			}
			f.Close()
			done++
		}
		out["done"] = done
	case "call":
		s := 0
		for i := 0; i < n; i++ {
			s += targetFunc(i)
		}
		out["done"], out["checksum"] = n, s
	case "callgrow":
		s := 0
		done := 0
		for i := 0; i < n; i++ {
			// каждая итерация — новая горутина с маленьким стеком: рост стека гарантирован
			ch := make(chan int)
			go func(i int) { ch <- targetGrow(i) }(i)
			s += <-ch
			done++
		}
		out["done"], out["checksum"] = done, s
	case "burst":
		k, _ := strconv.Atoi(os.Args[3])
		out["threads"], out["cpus"] = k, runtime.NumCPU()
		var wg sync.WaitGroup
		var mu sync.Mutex
		done := 0
		for t := 0; t < k; t++ {
			wg.Add(1)
			go func(t int) {
				defer wg.Done()
				runtime.LockOSThread()
				var set unix.CPUSet
				set.Set(t % runtime.NumCPU())
				unix.SchedSetaffinity(0, &set)
				c := n / k
				if t < n%k {
					c++
				}
				for i := 0; i < c; i++ {
					unix.Getppid()
				}
				mu.Lock()
				done += c
				mu.Unlock()
			}(t)
		}
		wg.Wait()
		out["done"] = done
	default:
		fmt.Fprintln(os.Stderr, "неизвестный режим", os.Args[1])
		os.Exit(2)
	}
	b, _ := json.Marshal(out)
	fmt.Printf("RESULT %s\n", b)
}
