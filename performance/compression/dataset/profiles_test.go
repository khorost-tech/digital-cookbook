package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestEventsSmallShape(t *testing.T) {
	ps := Generate(42, 1_000, 10, 2)
	ev := EventsSmall(ps, 500)
	if len(ev) != 500 {
		t.Fatalf("получено %d событий, ожидалось 500", len(ev))
	}
	// Смысл профиля — множество мелких похожих payload'ов: именно на них
	// словарное сжатие должно себя показать. Если события окажутся крупными,
	// сценарий dictionary потеряет смысл.
	for i, e := range ev {
		if len(e) == 0 || len(e) > 512 {
			t.Fatalf("событие %d имеет размер %d байт, ожидалось 1..512", i, len(e))
		}
	}
}

// splitLines разбивает буфер по '\n', отбрасывая пустой хвост от
// завершающего переноса строки.
func splitLines(b []byte) []string {
	lines := strings.Split(string(b), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// rowFieldValues разбирает построчную раскладку (TableRow) на значения по
// позиции поля: rowFieldValues(...)[i] — значения i-го столбца по всем
// записям, в порядке записей.
func rowFieldValues(row []byte, numFields int) [][]string {
	fields := make([][]string, numFields)
	for _, line := range splitLines(row) {
		parts := strings.Split(line, "\t")
		for i, p := range parts {
			fields[i] = append(fields[i], p)
		}
	}
	return fields
}

// colFieldValues разбирает колоночную раскладку (TableCol) на значения по
// позиции поля: colFieldValues(...)[i] — значения i-го блока (все записи
// подряд для одного поля).
func colFieldValues(col []byte, numFields, numRecords int) [][]string {
	lines := splitLines(col)
	fields := make([][]string, numFields)
	for i := 0; i < numFields; i++ {
		start := i * numRecords
		fields[i] = append([]string(nil), lines[start:start+numRecords]...)
	}
	return fields
}

func TestTableRowColSameContent(t *testing.T) {
	ps := Generate(42, 1_000, 10, 2)
	row := TableRow(ps)
	col := TableCol(ps)
	if len(row) == 0 || len(col) == 0 {
		t.Fatal("пустая раскладка")
	}

	const numFields = 6 // id, sku, title, price_cents, category, color
	rowFields := rowFieldValues(row, numFields)
	colFields := colFieldValues(col, numFields, len(ps))

	// Обе раскладки обязаны нести одни и те же значения в каждом поле, причём
	// в одном и том же порядке записей: TableRow обходит ps построчно, а
	// TableCol — блоками по полю, но внутри каждого блока идёт тот же самый
	// проход "for _, p := range ps", что и в TableRow. Раскладки различаются
	// порядком байт в файле (расположением полей друг относительно друга),
	// а не порядком записей внутри поля — поэтому сравниваем слайсы значений
	// позиционно, а не как мультимножества. Так тест ловит не только пропажу
	// или подмену значения, но и порчу порядка записей внутри блока (например,
	// обратный обход одного поля в TableCol расфазирует записи между полями,
	// хотя мультимножество значений в каждом поле останется прежним).
	for i := 0; i < numFields; i++ {
		if !reflect.DeepEqual(colFields[i], rowFields[i]) {
			t.Fatalf("поле %d: последовательность значений разошлась между построчной и колоночной раскладкой", i)
		}
	}
}
