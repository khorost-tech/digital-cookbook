// Package plugin регистрирует ctxloss как module plugin golangci-lint:
// `golangci-lint custom` собирает свой бинарь, в котором ctxloss — обычный
// линтер конфига (linters.settings.custom).
package plugin

import (
	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"

	"tech.khorost/golangci/analyzer/ctxloss"
)

func init() {
	register.Plugin("ctxloss", New)
}

// New — конструктор плагина; настроек у правила нет.
func New(any) (register.LinterPlugin, error) {
	return plugin{}, nil
}

type plugin struct{}

func (plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{ctxloss.Analyzer}, nil
}

// GetLoadMode — правилу нужны типы: оно узнаёт context.Context по типу, а не по имени.
func (plugin) GetLoadMode() string {
	return register.LoadModeTypesInfo
}
