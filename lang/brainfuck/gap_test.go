package brainfuck

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
	"unicode"
)

// TestEquivalence — фундамент всего замера: для каждой задачи brainfuck-версия
// и Go-версия дают ОДИН И ТОТ ЖЕ вывод на одном входе. Пока это зелено, обе
// версии решают одну задачу, и сравнивать их размер честно. Заодно это
// исполняет интерпретатор на реальных программах.
func TestEquivalence(t *testing.T) {
	for _, task := range Tasks {
		t.Run(task.Name, func(t *testing.T) {
			bfOut, err := run(task.BF, task.Input)
			if err != nil {
				t.Fatalf("brainfuck: %v", err)
			}
			var goOut bytes.Buffer
			if err := task.Go(bytes.NewReader(task.Input), &goOut); err != nil {
				t.Fatalf("go: %v", err)
			}
			if !bytes.Equal(bfOut, goOut.Bytes()) {
				t.Fatalf("вывод разошёлся: brainfuck=%q go=%q", bfOut, goOut.Bytes())
			}
		})
	}
}

// bfMetrics считает по программе на brainfuck значимые команды и число циклов.
func bfMetrics(prog string) (cmds, loops int) {
	for _, c := range prog {
		switch c {
		case '+', '-', '<', '>', '.', ',', '[', ']':
			cmds++
			if c == '[' {
				loops++
			}
		}
	}
	return cmds, loops
}

// goFuncMetrics достаёт исходник Go-функции ПРЯМО из gap.go и считает по нему
// значимые символы тела и число различных имён (идентификаторов). Замеряется
// именно тот код, что исполнялся в TestEquivalence, — разойтись они не могут.
func goFuncMetrics(t *testing.T, name string) (chars, names int) {
	t.Helper()
	src, err := os.ReadFile("gap.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "gap.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatalf("функция %s не найдена в gap.go", name)
	}
	body := src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset]
	for _, r := range string(body) {
		if !unicode.IsSpace(r) {
			chars++
		}
	}
	seen := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name != "_" {
			seen[id.Name] = true
		}
		return true
	})
	return chars, len(seen)
}

// TestExpressivenessGap печатает таблицу разрыва. Запуск:
//
//	go test -run ExpressivenessGap -v
//
// Числа воспроизводимы: команды brainfuck и символы Go считаются из исходников,
// а не вписаны. Мощность у версий одна (см. TestEquivalence) — таблица показывает
// цену этой мощности в выразительности.
func TestExpressivenessGap(t *testing.T) {
	var lines []string
	lines = append(lines,
		fmt.Sprintf("%-34s %8s %8s %7s   %8s %8s %8s", "задача", "bf-команд", "Go-симв.", "bf/Go", "Go имён", "bf имён", "bf циклов"))
	for _, task := range Tasks {
		cmds, loops := bfMetrics(task.BF)
		chars, names := goFuncMetrics(t, task.GoSrc)
		lines = append(lines,
			fmt.Sprintf("%-34s %8d %8d %6.2f   %8d %8d %8d",
				task.Name, cmds, chars, float64(cmds)/float64(chars), names, 0, loops))
	}
	// Один блок, чтобы вывод не рвался префиксами testing по строкам.
	t.Log("\n" + join(lines, "\n"))
}

func join(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
