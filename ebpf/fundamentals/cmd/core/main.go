// Загрузчик оси CO-RE: нагрузка делает getppid; программа читает поля
// task_struct текущего процесса через CO-RE-релокации.
//
//	core tgid | guarded | old
package main

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel core ../../bpf/core.c -- -I/usr/include/aarch64-linux-gnu -I../../bpf
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel coreOld ../../bpf/core_old.c -- -I/usr/include/aarch64-linux-gnu -I../../bpf

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"

	"stand/internal/loadproc"
)

func main() {
	variant := os.Args[1]
	res := map[string]any{"variant": variant}
	defer func() {
		b, _ := json.Marshal(res)
		fmt.Printf("RESULT %s\n", b)
	}()
	fail := func(stage string, err error) bool {
		if err != nil {
			res["stage"], res["error"] = stage, err.Error()
			return true
		}
		return false
	}
	if fail("setup", rlimit.RemoveMemlock()) {
		return
	}
	proc, err := loadproc.Start("burst", "100", "1")
	if fail("setup", err) {
		return
	}
	defer proc.Run() // нагрузка не должна остаться висеть, если программа не загрузилась
	res["truth_tgid"] = proc.PID

	var spec *ebpf.CollectionSpec
	if variant == "old" {
		spec, err = loadCoreOld()
	} else {
		spec, err = loadCore()
	}
	if fail("setup", err) {
		return
	}
	if fail("setup", spec.Variables["target_tgid"].Set(uint32(proc.PID))) {
		return
	}
	name := map[string]string{"tgid": "core_tgid", "guarded": "core_state_guarded", "old": "core_state_old"}[variant]
	coll, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{})
	if fail("load", err) {
		return
	}
	defer coll.Close()
	l, err := link.Tracepoint("syscalls", "sys_enter_getppid", coll.Programs[name], nil)
	if fail("attach", err) {
		return
	}
	defer l.Close()
	res["stage"] = "loaded"
	load, exit, _ := proc.Run()
	res["load"], res["load_exit"] = load, exit
	out := coll.Maps["out"]
	read := func(slot uint32) int64 {
		var v int64
		out.Lookup(slot, &v)
		return v
	}
	switch variant {
	case "tgid":
		res["value"] = read(0)
	case "guarded":
		res["value"], res["branch"] = read(1), map[int64]string{0: "state", 1: "__state"}[read(2)]
	case "old":
		res["value"] = read(0)
	}
}
