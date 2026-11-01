// Загрузчик bpftrace-эквивалентов: запускает нагрузку, затем bpftrace со
// скриптом, где $PID заменён PID нагрузки; ждёт, пока зонды прицепятся
// («Attaching»), даёт нагрузке старт, останавливает bpftrace и разбирает @n.
//
//	bt SCRIPT MODE N [K]     SCRIPT: open | burst | uprobe
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"stand/internal/loadproc"
)

var scripts = map[string]string{
	"open":   `tracepoint:syscalls:sys_enter_openat /pid == $PID/ { @n = count(); }`,
	"burst":  `tracepoint:syscalls:sys_enter_getppid /pid == $PID/ { @n = count(); }`,
	"uprobe": `uprobe:/opt/stand/bin/load:main.targetFunc /pid == $PID/ { @n = count(); }`,
}

func main() {
	script, args := os.Args[1], os.Args[2:]
	res := map[string]any{"script": script}
	defer func() {
		b, _ := json.Marshal(res)
		fmt.Printf("RESULT %s\n", b)
	}()
	proc, err := loadproc.Start(args...)
	if err != nil {
		res["error"] = err.Error()
		return
	}
	prog := strings.ReplaceAll(scripts[script], "$PID", strconv.Itoa(proc.PID))
	res["program"] = scripts[script]
	bt := exec.Command("bpftrace", "-e", prog)
	stdout, _ := bt.StdoutPipe()
	bt.Stderr = bt.Stdout
	if err := bt.Start(); err != nil {
		res["error"] = err.Error()
		return
	}
	rd := bufio.NewReader(stdout)
	lines := make(chan string, 64)
	go func() {
		for {
			l, err := rd.ReadString('\n')
			if l != "" {
				lines <- l
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	var out []string
	for l := range lines { // «Attaching N probes...» — зонды на месте
		out = append(out, l)
		if strings.HasPrefix(l, "Attaching") || strings.HasPrefix(l, "Attached") {
			break
		}
	}
	time.Sleep(300 * time.Millisecond)
	load, exit, _ := proc.Run()
	res["load"], res["load_exit"] = load, exit
	bt.Process.Signal(syscall.SIGINT)
	for l := range lines {
		out = append(out, l)
	}
	bt.Wait()
	m := regexp.MustCompile(`@n: (\d+)`).FindStringSubmatch(strings.Join(out, ""))
	if m == nil {
		res["error"] = "нет @n в выводе bpftrace: " + strings.Join(out, "")
		return
	}
	res["counted"], _ = strconv.Atoi(m[1])
}
