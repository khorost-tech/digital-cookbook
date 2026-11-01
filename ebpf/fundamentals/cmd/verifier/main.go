// Загрузчик оси verifier: компилирует программу из bpf/verifier/NAME.c и
// пробует загрузить. Вердикт — на каком этапе остановилась: clang, verifier
// или принята; для отказа — строка-причина из сообщения.
//
//	verifier NAME
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"
)

func main() {
	name := os.Args[1]
	src := "/opt/stand/bpf/verifier/" + name + ".c"
	obj := "/tmp/verifier-" + name + ".o"
	res := map[string]any{"name": name}
	defer func() {
		b, _ := json.Marshal(res)
		fmt.Printf("RESULT %s\n", b)
	}()
	head, _ := os.ReadFile(src)
	first := strings.SplitN(string(head), "\n", 2)[0]
	res["expect"] = strings.Fields(strings.TrimPrefix(first, "// expect:"))[0]

	out, err := exec.Command("clang", "-O2", "-g", "-target", "bpf", "-I/usr/include/aarch64-linux-gnu",
		"-c", src, "-o", obj).CombinedOutput()
	if err != nil {
		res["stage"] = "clang"
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "error") {
				res["reason"] = strings.TrimSpace(l[strings.Index(l, "error"):])
				break
			}
		}
		return
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		res["stage"], res["reason"] = "setup", err.Error()
		return
	}
	spec, err := ebpf.LoadCollectionSpec(obj)
	if err != nil {
		res["stage"], res["reason"] = "setup", err.Error()
		return
	}
	coll, err := ebpf.NewCollection(spec)
	if err == nil {
		defer coll.Close()
		res["stage"] = "accepted"
		if name == "bpf_loop" { // загрузилась — ещё не значит, что выполнила заданное число итераций
			res["run"] = runLoop(coll)
		}
		return
	}
	res["stage"] = "verifier"
	var ve *ebpf.VerifierError
	if errors.As(err, &ve) {
		res["log_tail"] = ve.Log[max(0, len(ve.Log)-4):]
		res["reason"] = cause(ve.Log)
	} else {
		res["reason"] = err.Error()
	}
}

// runLoop исполняет принятую программу bpf_loop: n в карту, программа на
// tracepoint getppid, один вызов getppid от загрузчика. Возвращает, сколько
// итераций выполнено и что вернул помощник.
func runLoop(coll *ebpf.Collection) map[string]any {
	m := coll.Maps["m"]
	l, err := link.Tracepoint("syscalls", "sys_enter_getppid", coll.Programs["prog"], nil)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer l.Close()
	const n = 100000
	m.Put(uint32(0), uint64(n))
	unix.Getppid()
	var iters, ret uint64
	m.Lookup(uint32(1), &iters)
	m.Lookup(uint32(2), &ret)
	return map[string]any{"n": n, "iterations": iters, "ret": int64(ret)}
}

// cause — строка журнала verifier с причиной отказа: последняя содержательная,
// без счётчиков обработанных инструкций и времени проверки.
func cause(log []string) string {
	for i := len(log) - 1; i >= 0; i-- {
		l := strings.TrimSpace(log[i])
		if l == "" || strings.HasPrefix(l, "processed ") || strings.HasPrefix(l, "verification time") ||
			strings.HasPrefix(l, "stack depth") || strings.HasPrefix(l, "max_states_per_insn") {
			continue
		}
		return l
	}
	return ""
}
