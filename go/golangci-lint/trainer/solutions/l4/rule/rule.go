// Package rule — эталон уровня 4: правило nopanic.
package rule

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// Analyzer — правило nopanic.
var Analyzer = &analysis.Analyzer{
	Name: "nopanic",
	Doc:  "сообщает о вызове panic вне пакета main",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if pass.Pkg.Name() == "main" {
		return nil, nil
	}
	for _, f := range pass.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if b, ok := pass.TypesInfo.Uses[id].(*types.Builtin); ok && b.Name() == "panic" {
				pass.Reportf(call.Pos(), "panic в библиотечном пакете: верните ошибку")
			}
			return true
		})
	}
	return nil, nil
}
