package main

// Чтение и запись сохранённой истории. Вынесено отдельно, потому что
// этим пользуются и замер, и replay-тесты.

import (
	"fmt"
	"os"

	"go.temporal.io/api/history/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// saveHistory пишет историю в файл.
//
// Сериализация — protojson, а НЕ обычный encoding/json: у
// protobuf-сообщений Temporal своё представление oneof-полей, и
// стандартный json.Marshal даёт файл, из которого события восстановятся
// без полезной нагрузки. Тот же формат читает и Web UI, и штатный
// реплеер SDK.
func saveHistory(path string, h *history.History) error {
	blob, err := protojson.Marshal(h)
	if err != nil {
		return fmt.Errorf("сериализация истории: %w", err)
	}
	return os.WriteFile(path, blob, 0o600)
}

// loadHistory читает файл, записанный saveHistory.
func loadHistory(path string) (*history.History, error) {
	blob, err := os.ReadFile(path) //nolint:gosec // путь задаётся стендом
	if err != nil {
		return nil, err
	}
	var h history.History
	if err := protojson.Unmarshal(blob, &h); err != nil {
		return nil, fmt.Errorf("разбор истории: %w", err)
	}
	return &h, nil
}
