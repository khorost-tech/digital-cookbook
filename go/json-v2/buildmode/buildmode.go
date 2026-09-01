// Package buildmode отвечает на вопрос «в каком режиме собран ЭТОТ бинарник».
//
// GOEXPERIMENT=nojsonv2 — флаг СБОРКИ, а не рантайма. Значит бенч, собранный не
// в том режиме, молча померяет не то: «разницы нет» и «флаг не применился»
// выглядят одинаково. Поэтому режим берётся не из переменной окружения (её могли
// не передать в сборку), а из самого бинарника.
package buildmode

import (
	"runtime/debug"
	"strings"
)

type Mode string

const (
	ModeJSONv2   Mode = "v1-on-v2" // умолчание 1.27: encoding/json поверх v2
	ModeNoJSONv2 Mode = "v1-old"   // откат на прежний движок
)

func parse(goexperiment string) Mode {
	for _, part := range strings.Split(goexperiment, ",") {
		if strings.TrimSpace(part) == "nojsonv2" {
			return ModeNoJSONv2
		}
	}
	return ModeJSONv2
}

// Raw возвращает строку GOEXPERIMENT, вшитую в бинарник при сборке.
func Raw() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "GOEXPERIMENT" {
			return s.Value
		}
	}
	return ""
}

// Current возвращает режим, в котором собран текущий бинарник.
func Current() Mode { return parse(Raw()) }
