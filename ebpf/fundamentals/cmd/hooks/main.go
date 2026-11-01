// Загрузчик оси hooks: запускает нагрузку (internal/loadproc), узнаёт её PID, загружает программы
// с этим PID, цепляет одну программу и даёт нагрузке старт. Счёт программы
// сверяется со счётом нагрузки.
//
//	hooks HOOK MODE N     HOOK: tracepoint | kprobe | fentry | uprobe | uretprobe
package main

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel hooks ../../bpf/hooks.c -- -I/usr/include/aarch64-linux-gnu

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"

	"stand/internal/loadproc"
)

var slot = map[string]uint32{"tracepoint": 0, "kprobe": 1, "fentry": 2, "uprobe": 3, "uretprobe": 4}

// symbol — функция нагрузки, на которую цепляется uprobe в этом режиме.
func symbol(mode string) string {
	if mode == "callgrow" {
		return "main.targetGrow"
	}
	return "main.targetFunc"
}

func main() {
	hook, mode, n := os.Args[1], os.Args[2], os.Args[3]
	res := map[string]any{"hook": hook, "mode": mode}
	defer func() {
		b, _ := json.Marshal(res)
		fmt.Printf("RESULT %s\n", b)
	}()
	must := func(err error) {
		if err != nil {
			res["error"] = err.Error()
			b, _ := json.Marshal(res)
			fmt.Printf("RESULT %s\n", b)
			os.Exit(1)
		}
	}
	must(rlimit.RemoveMemlock())

	proc, err := loadproc.Start(mode, n)
	must(err)
	pid := proc.PID

	spec, err := loadHooks()
	must(err)
	must(spec.Variables["target_tgid"].Set(uint32(pid)))
	var objs hooksObjects
	must(spec.LoadAndAssign(&objs, nil))
	defer objs.Close()

	var l link.Link
	switch hook {
	case "tracepoint":
		l, err = link.Tracepoint("syscalls", "sys_enter_openat", objs.TpOpenat, nil)
	case "kprobe":
		l, err = link.Kprobe("do_sys_openat2", objs.KprobeOpenat2, nil)
	case "fentry":
		l, err = link.AttachTracing(link.TracingOptions{Program: objs.FentryOpenat2})
	case "uprobe", "uretprobe":
		ex, e := link.OpenExecutable(loadproc.Bin)
		must(e)
		opts := &link.UprobeOptions{PID: pid}
		if hook == "uprobe" {
			l, err = ex.Uprobe(symbol(mode), objs.UprobeTarget, opts)
		} else {
			l, err = ex.Uretprobe(symbol(mode), objs.UretprobeTarget, opts)
		}
	}
	must(err)
	defer l.Close()

	loadRes, exit, crash := proc.Run()
	var c uint64
	must(objs.Counts.Lookup(slot[hook], &c))
	res["counted"], res["pid"] = c, pid
	res["load"] = loadRes
	res["load_exit"] = exit
	if crash != "" {
		res["load_crash"] = crash
	}
}
