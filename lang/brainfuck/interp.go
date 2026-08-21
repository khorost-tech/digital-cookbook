// Package brainfuck — интерпретатор языка brainfuck на Go.
//
// Восемь команд, лента байтовых ячеек, указатель. Этого достаточно для
// тьюринг-полноты, а значит, эта сотня строк — универсальная машина: она
// исполняет любую программу на brainfuck, а на brainfuck выразимо всё
// вычислимое. Стенд к статье «Тьюринг-полнота и выразительность».
package brainfuck

import (
	"errors"
	"io"
)

// Ошибки исполнения.
var (
	// ErrUnbalanced — скобки [ ] не сбалансированы.
	ErrUnbalanced = errors.New("brainfuck: несбалансированные скобки")
	// ErrPointerRange — указатель ушёл левее начала ленты.
	ErrPointerRange = errors.New("brainfuck: указатель за левой границей ленты")
)

// Machine — состояние исполнителя: лента и указатель на текущую ячейку.
// Лента растёт вправо по мере надобности; ячейки — байты с переполнением
// по модулю 256 (как и задумано в brainfuck).
type Machine struct {
	tape []byte
	ptr  int
	in   io.Reader
	out  io.Writer
}

// New создаёт машину, читающую байты из in (команда «,») и пишущую в out («.»).
func New(in io.Reader, out io.Writer) *Machine {
	return &Machine{tape: make([]byte, 1), in: in, out: out}
}

// Run исполняет программу prog. Все символы, кроме восьми команд, игнорируются
// (в brainfuck они служат комментариями). Возвращает первую ошибку исполнения.
func (m *Machine) Run(prog string) error {
	jumps, err := matchBrackets(prog)
	if err != nil {
		return err
	}
	buf := make([]byte, 1)
	for pc := 0; pc < len(prog); pc++ {
		switch prog[pc] {
		case '>':
			m.ptr++
			if m.ptr == len(m.tape) {
				m.tape = append(m.tape, 0)
			}
		case '<':
			if m.ptr == 0 {
				return ErrPointerRange
			}
			m.ptr--
		case '+':
			m.tape[m.ptr]++
		case '-':
			m.tape[m.ptr]--
		case '.':
			buf[0] = m.tape[m.ptr]
			if _, err := m.out.Write(buf); err != nil {
				return err
			}
		case ',':
			switch _, err := io.ReadFull(m.in, buf); {
			case err == nil:
				m.tape[m.ptr] = buf[0]
			case errors.Is(err, io.EOF):
				m.tape[m.ptr] = 0 // конец ввода → 0 (распространённая конвенция)
			default:
				return err
			}
		case '[':
			if m.tape[m.ptr] == 0 {
				pc = jumps[pc]
			}
		case ']':
			if m.tape[m.ptr] != 0 {
				pc = jumps[pc]
			}
		}
	}
	return nil
}

// matchBrackets заранее считает для каждой скобки позицию парной ей — чтобы
// прыжок цикла был O(1), а не поиском по строке на каждой итерации.
func matchBrackets(prog string) ([]int, error) {
	jumps := make([]int, len(prog))
	var stack []int
	for i := 0; i < len(prog); i++ {
		switch prog[i] {
		case '[':
			stack = append(stack, i)
		case ']':
			if len(stack) == 0 {
				return nil, ErrUnbalanced
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			jumps[open] = i
			jumps[i] = open
		}
	}
	if len(stack) != 0 {
		return nil, ErrUnbalanced
	}
	return jumps, nil
}
