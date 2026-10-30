// Package ctxloss — анализатор: context.Background() или context.TODO() внутри
// функции, у которой уже есть параметр context.Context.
//
// Такой вызов отрезает срок и отмену вызывающего: нижнее звено продолжит работу,
// когда клиент давно ушёл (опыт «deadline в цепочке» в статье про gRPC).
// context.WithoutCancel(ctx) срок и отмену ТОЖЕ убирает — сохраняет только
// значения контекста. Правило его пропускает не потому, что он сохраняет срок,
// а потому, что это явное, видимое в коде намерение отделить фоновую работу
// (например, после ответа). Такой работе обычно задают собственный срок:
// context.WithTimeout(context.WithoutCancel(ctx), …).
package ctxloss

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer — правило ctxloss.
var Analyzer = &analysis.Analyzer{
	Name:     "ctxloss",
	Doc:      "сообщает о context.Background()/TODO() в функции, которая уже получила context.Context",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	// Обход по вызовам со стеком: вызов считается, если хоть одна объемлющая
	// функция — объявление или литерал — получила context.Context. Так видны и
	// замыкания внутри функции с ctx, и литерал со своим ctx внутри функции без него.
	ins.WithStack([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		name := rootContext(pass, n.(*ast.CallExpr))
		if name == "" || !insideContextFunc(pass, stack) {
			return true
		}
		pass.Reportf(n.Pos(), "context.%s() в функции с параметром context.Context: "+
			"срок и отмена вызывающего теряются; передайте ctx, а если работа намеренно фоновая — "+
			"context.WithoutCancel(ctx) со своим сроком", name)
		return true
	})
	return nil, nil
}

// insideContextFunc — есть ли в стеке функция с параметром context.Context.
func insideContextFunc(pass *analysis.Pass, stack []ast.Node) bool {
	for _, s := range stack {
		var ft *ast.FuncType
		switch f := s.(type) {
		case *ast.FuncDecl:
			ft = f.Type
		case *ast.FuncLit:
			ft = f.Type
		}
		if ft != nil && hasContextParam(pass, ft) {
			return true
		}
	}
	return false
}

// hasContextParam — есть ли у функции параметр типа context.Context.
func hasContextParam(pass *analysis.Pass, ft *ast.FuncType) bool {
	for _, f := range ft.Params.List {
		if isContext(pass.TypesInfo.TypeOf(f.Type)) {
			return true
		}
	}
	return false
}

func isContext(t types.Type) bool {
	// type Ctx = context.Context для go/types — *types.Alias: снимаем алиас до
	// именованного типа, иначе правило молчит на таком параметре.
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// rootContext — "Background" или "TODO", если вызов — context.Background()/TODO().
func rootContext(pass *analysis.Pass, call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "context" {
		return ""
	}
	if fn.Name() == "Background" || fn.Name() == "TODO" {
		return fn.Name()
	}
	return ""
}
