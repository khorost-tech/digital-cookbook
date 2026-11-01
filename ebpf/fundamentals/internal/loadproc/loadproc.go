// Package loadproc запускает нагрузку (cmd/load) и ведёт протокол с ней:
// READY <pid> → загрузчик цепляет программы → "go" → RESULT <json>.
package loadproc

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const Bin = "/opt/stand/bin/load"

type Proc struct {
	PID    int
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	rd     *bufio.Reader
	stderr strings.Builder
}

// Start запускает нагрузку и ждёт READY. GODEBUG=adaptivestackstart=0: начальный
// стек горутин не подстраивается под прошлые — рост стека воспроизводим.
func Start(args ...string) (*Proc, error) {
	p := &Proc{cmd: exec.Command(Bin, args...)}
	p.cmd.Env = append(os.Environ(), "GODEBUG=adaptivestackstart=0")
	p.cmd.Stderr = &p.stderr
	var err error
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = p.cmd.Start(); err != nil {
		return nil, err
	}
	p.rd = bufio.NewReader(out)
	line, err := p.rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	p.PID, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "READY ")))
	return p, err
}

// Run даёт старт и ждёт конца: счёт нагрузки, код выхода, первая строка падения.
func (p *Proc) Run() (result map[string]any, exit string, crash string) {
	io.WriteString(p.stdin, "go\n")
	for {
		s, err := p.rd.ReadString('\n')
		if strings.HasPrefix(s, "RESULT ") {
			json.Unmarshal([]byte(strings.TrimPrefix(s, "RESULT ")), &result)
		}
		if err != nil {
			break
		}
	}
	exit = "ok"
	if err := p.cmd.Wait(); err != nil {
		exit = err.Error()
		for _, l := range strings.Split(p.stderr.String(), "\n") {
			if strings.Contains(l, "fatal error") || strings.HasPrefix(l, "panic") || strings.Contains(l, "unexpected return pc") {
				crash = strings.TrimSpace(l)
				break
			}
		}
	}
	return
}
