// Загрузчик оси maps: нагрузка burst N K (getppid из K потоков на разных CPU),
// одна программа-счётчик на tracepoint sys_enter_getppid, счёт против эталона.
//
//	maps VARIANT N K [RINGSIZE]
//	VARIANT: hash-racy | hash-atomic | percpu | ring | ring-read
//	ring      — буфер RINGSIZE байт, читается после нагрузки;
//	ring-read — тот же буфер, читается во время нагрузки.
package main

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel maps ../../bpf/maps.c -- -I/usr/include/aarch64-linux-gnu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"

	"stand/internal/loadproc"
)

func main() {
	variant, n, k := os.Args[1], os.Args[2], os.Args[3]
	res := map[string]any{"variant": variant}
	must := func(err error) {
		if err != nil {
			res["error"] = err.Error()
			b, _ := json.Marshal(res)
			fmt.Printf("RESULT %s\n", b)
			os.Exit(1)
		}
	}
	must(rlimit.RemoveMemlock())
	proc, err := loadproc.Start("burst", n, k)
	must(err)

	spec, err := loadMaps()
	must(err)
	must(spec.Variables["target_tgid"].Set(uint32(proc.PID)))
	if len(os.Args) > 4 {
		size, _ := strconv.Atoi(os.Args[4])
		spec.Maps["events"].MaxEntries = uint32(size)
		res["ring_bytes"] = size
	}
	var objs mapsObjects
	must(spec.LoadAndAssign(&objs, nil))
	defer objs.Close()

	zero := uint32(0)
	must(objs.HashCounts.Put(zero, uint64(0)))
	prog := map[string]*ebpf.Program{"hash-racy": objs.HashRacy, "hash-atomic": objs.HashAtomic,
		"percpu": objs.Percpu, "ring": objs.Ring, "ring-read": objs.Ring}[variant]
	l, err := link.Tracepoint("syscalls", "sys_enter_getppid", prog, nil)
	must(err)
	defer l.Close()

	var received int
	var rd *ringbuf.Reader
	var wg sync.WaitGroup
	if variant == "ring" || variant == "ring-read" {
		rd, err = ringbuf.NewReader(objs.Events)
		must(err)
	}
	if variant == "ring-read" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if _, err := rd.Read(); err != nil {
					return
				}
				received++
			}
		}()
	}

	loadRes, exit, _ := proc.Run()
	res["load"], res["load_exit"] = loadRes, exit

	switch variant {
	case "hash-racy", "hash-atomic":
		var c uint64
		must(objs.HashCounts.Lookup(zero, &c))
		res["counted"] = c
	case "percpu":
		var per []uint64
		must(objs.PercpuCounts.Lookup(zero, &per))
		var sum uint64
		used := 0
		for _, v := range per {
			sum += v
			if v > 0 {
				used++
			}
		}
		res["counted"], res["cpus_used"] = sum, used
	case "ring", "ring-read":
		if variant == "ring" { // нагрузка закончилась — вычитать всё, что успело лечь в буфер
			rd.SetDeadline(time.Now().Add(time.Second))
			for {
				if _, err := rd.Read(); err != nil {
					if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, ringbuf.ErrClosed) {
						break
					}
					must(err)
				}
				received++
			}
		} else {
			time.Sleep(500 * time.Millisecond)
			rd.Close()
			wg.Wait()
		}
		var d uint64
		must(objs.RingDrops.Lookup(zero, &d))
		res["counted"], res["dropped"] = received, d
	}
	b, _ := json.Marshal(res)
	fmt.Printf("RESULT %s\n", b)
}
