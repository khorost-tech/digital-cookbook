package rule_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"tech.khorost/golangci/trainer/solutions/l4/rule"
)

// Тест — грейдер уровня: lib обязан дать находки, app (пакет main) — нет.
func TestNopanic(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), rule.Analyzer, "lib", "app")
}
