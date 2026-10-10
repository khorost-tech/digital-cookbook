package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
)

// Replay-тест против сохранённой истории — дешёвая защита от ломающих
// изменений: он не требует ни сервера, ни воркера, и его можно держать
// в CI. Именно этим ловят несовместимость ДО выката.
func TestFixedWorkflowReplaysCleanly(t *testing.T) {
	path := filepath.Join("histories", "fixed-demo.json")
	h, err := loadHistory(path)
	if err != nil {
		t.Skipf("нет сохранённой истории %s — сначала прогоните scripts/02-determinism.sh", path)
	}
	// Пустая история прошла бы проверку, ничего не проверив.
	require.NotEmpty(t, h.Events, "история пуста")

	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(FixedWorkflow)
	require.NoError(t, r.ReplayWorkflowHistory(nil, h))
}

// Зеркальный тест: история сломанного воркфлоу НЕ должна проигрываться
// чисто. Если он вдруг пройдёт — значит контрпример перестал быть
// контрпримером, и статья опирается на утверждение, которого больше нет.
func TestBrokenWorkflowFailsReplay(t *testing.T) {
	path := filepath.Join("histories", "broken-demo.json")
	h, err := loadHistory(path)
	if err != nil {
		t.Skipf("нет сохранённой истории %s — сначала прогоните scripts/02-determinism.sh", path)
	}
	require.NotEmpty(t, h.Events, "история пуста")

	// История записана процессом с DEMO_BRANCH=long; проигрываем с другим
	// окружением — ровно та ситуация, когда воркер перезапустили, а
	// внешнее состояние изменилось.
	t.Setenv("DEMO_BRANCH", "short")

	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(BrokenWorkflow)
	err = r.ReplayWorkflowHistory(nil, h)
	require.Error(t, err, "сломанный воркфлоу проигрался чисто — контрпример перестал работать")
	t.Logf("ожидаемая ошибка replay: %v", err)
}
