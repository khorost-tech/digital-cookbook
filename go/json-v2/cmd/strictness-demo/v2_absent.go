//go:build !goexperiment.jsonv2

// Парный файл к v2_strict.go для отката (GOEXPERIMENT=nojsonv2). Под откатом
// пакет encoding/json/v2 исключён build-ограничениями целиком — это не деталь
// сборки, а факт для статьи: откат на прежний движок отнимает не только
// строгость, но и саму возможность сравнить её с v2 API в этой же сборке.
// Третья колонка поэтому не "не измерена", а "недоступна" — n/a, а не пустое
// место.
package main

// v2Available: под откатом пакета encoding/json/v2 в сборке нет вовсе.
const v2Available = false

// v2Verdict здесь ничего не решает — колонка v2 API недоступна целиком.
func v2Verdict(data string) string {
	return "n/a"
}
