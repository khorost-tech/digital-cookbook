package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// Тестовое окружение перематывает таймеры: Sleep(24h) не ждёт сутки.
// Тест меряет СОБСТВЕННУЮ длительность и падает, если время реально
// ждали, — иначе «проходит мгновенно» осталось бы утверждением без
// проверки, и тест был бы зелёным даже при сломанной перемотке.
func TestDayLongWorkflowSkipsTime(t *testing.T) {
	started := time.Now()

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterActivity(&SlowActs{Work: 10 * time.Millisecond})

	env.ExecuteWorkflow(DayLongWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var res string
	require.NoError(t, env.GetWorkflowResult(&res))
	require.Equal(t, "сутки прошли", res)

	elapsed := time.Since(started)
	require.Less(t, elapsed, 10*time.Second,
		"тест шёл %s — время не перематывалось", elapsed)
	t.Logf("логические сутки пройдены за %s реального времени", elapsed)
}
