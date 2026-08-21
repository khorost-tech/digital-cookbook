package brainfuck

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// run — короткий помощник: исполнить программу на заданном вводе, вернуть вывод.
func run(prog string, input []byte) ([]byte, error) {
	var out bytes.Buffer
	err := New(bytes.NewReader(input), &out).Run(prog)
	return out.Bytes(), err
}

func TestHelloWorld(t *testing.T) {
	// Тест не верит программе на слово — он её исполняет. Если бы «Hello World»
	// был набран неверно, здесь бы и всплыло.
	out, err := run(Tasks[3].BF, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "Hello World!\n" {
		t.Fatalf("ожидали %q, получили %q", "Hello World!\n", got)
	}
}

func TestCommentsIgnored(t *testing.T) {
	// Всё, кроме восьми команд, — комментарий. Программа с пояснениями между
	// командами должна работать ровно как без них.
	out, err := run("+ увеличить + ещё раз . вывести", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != 2 {
		t.Fatalf("ожидали байт 2, получили %v", out)
	}
}

func TestTapeGrows(t *testing.T) {
	// Лента начинается с одной ячейки и растёт вправо: 300 сдвигов не должны
	// упереться в границу.
	if _, err := run(strings.Repeat(">", 300)+"+.", nil); err != nil {
		t.Fatalf("лента не выросла: %v", err)
	}
}

func TestEOFGivesZero(t *testing.T) {
	// Чтение за концом ввода кладёт в ячейку 0.
	out, err := run(",.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != 0 {
		t.Fatalf("ожидали 0 на EOF, получили %v", out)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		prog string
		want error
	}{
		{"скобка не закрыта", "+[+", ErrUnbalanced},
		{"лишняя закрывающая", "+]", ErrUnbalanced},
		{"указатель левее нуля", "<", ErrPointerRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := run(c.prog, nil); !errors.Is(err, c.want) {
				t.Fatalf("ожидали %v, получили %v", c.want, err)
			}
		})
	}
}
