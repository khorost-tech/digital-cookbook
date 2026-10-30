// Package rule — уровень 4: своё правило на go/analysis.
//
// Правило nopanic: вызов встроенной panic в пакете, который не main.
// Библиотека не должна ронять процесс вызывающего — она возвращает ошибку.
package rule

import (
	"golang.org/x/tools/go/analysis"
)

// Analyzer — правило nopanic.
var Analyzer = &analysis.Analyzer{
	Name: "nopanic",
	Doc:  "сообщает о вызове panic вне пакета main",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	// Задача: пройти по файлам пакета (pass.Files), найти вызовы встроенной panic
	// и на каждый сообщить pass.Reportf(позиция, "panic в библиотечном пакете: верните ошибку"),
	// если пакет не main (pass.Pkg.Name()).
	// Подсказка: встроенную panic отличает тип объекта — *types.Builtin в pass.TypesInfo.Uses;
	// функция с именем panic, объявленная в пакете, — не встроенная.
	return nil, nil
}
