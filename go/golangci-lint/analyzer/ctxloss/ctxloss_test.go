package ctxloss_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"tech.khorost/golangci/analyzer/ctxloss"
)

func TestCtxloss(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), ctxloss.Analyzer, "a")
}
