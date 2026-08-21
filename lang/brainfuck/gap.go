package brainfuck

import "io"

// Task — одна задача, решённая ДВАЖДЫ: на brainfuck и на Go. Обе версии
// вычисляют одно и то же (это проверяет тест, прогоняя обе на Input и сверяя
// вывод) — поэтому сравнивать их размер и «именованность» честно: мощность
// одинакова, разошлась только выразительность.
type Task struct {
	Name  string
	BF    string                           // программа на brainfuck
	Go    func(io.Reader, io.Writer) error // эквивалент на Go
	GoSrc string                           // имя Go-функции — по нему тест достаёт её исходник из gap.go
	Input []byte                           // общий вход для обеих версий
}

// Tasks — четыре задачи с разным поведением размера. На арифметике brainfuck
// выходит КОРОЧЕ Go в символах (у Go — обработка ошибок и возня с байтами), и
// это важный результат: разрыв выразительности живёт не в длине. Только «Hello
// World» раздувает brainfuck — константу там приходится собирать арифметикой.
// Байтовые задачи работают со значениями байтов напрямую (3,4 → 7), без ASCII.
var Tasks = []Task{
	{
		Name:  "эхо ввода (cat)",
		BF:    ",[.,]",
		Go:    goCat,
		GoSrc: "goCat",
		Input: []byte("brainfuck"),
	},
	{
		Name:  "удвоить байт (a → 2a)",
		BF:    ",[->++<]>.",
		Go:    goDouble,
		GoSrc: "goDouble",
		Input: []byte{21},
	},
	{
		Name:  "сложить два байта (a,b → a+b)",
		BF:    ",>,[-<+>]<.",
		Go:    goAdd,
		GoSrc: "goAdd",
		Input: []byte{3, 4},
	},
	{
		Name: "напечатать «Hello World!\\n»",
		BF: "++++++++[>++++[>++>+++>+++>+<<<<-]>+>+>->>+[<]<-]>>.>---.+++++++.." +
			"+++.>>.<-.<.+++.------.--------.>>+.>++.",
		Go:    goHello,
		GoSrc: "goHello",
		Input: nil,
	},
}

// Ниже — те же задачи на Go. Это НЕ учебные заглушки: именно эти функции тест
// прогоняет и сверяет с brainfuck-версией, и именно их исходник он измеряет.

func goCat(r io.Reader, w io.Writer) error {
	_, err := io.Copy(w, r)
	return err
}

func goDouble(r io.Reader, w io.Writer) error {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	_, err := w.Write([]byte{b[0] * 2})
	return err
}

func goAdd(r io.Reader, w io.Writer) error {
	var ab [2]byte
	if _, err := io.ReadFull(r, ab[:]); err != nil {
		return err
	}
	_, err := w.Write([]byte{ab[0] + ab[1]})
	return err
}

func goHello(_ io.Reader, w io.Writer) error {
	_, err := io.WriteString(w, "Hello World!\n")
	return err
}
