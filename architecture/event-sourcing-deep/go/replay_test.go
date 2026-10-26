package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
)

// update — перезаписать testdata/expected.json результатом ТЕКУЩЕГО кода.
// Используется РОВНО один раз, при генерации golden-файла на заведомо
// исправном Apply: `go test -run TestReplay -update`. В обычном прогоне
// (без флага) тест ТОЛЬКО сравнивает, никогда не перезаписывает golden.
var update = flag.Bool("update", false, "перезаписать testdata/expected.json текущим результатом")

// tankState — то, что мы фиксируем как ожидаемый итог по стриму:
// достаточно для сравнения, без раскрытия внутренней структуры Tank.
type tankState struct {
	Registered bool    `json:"registered"`
	Liters     float64 `json:"liters"`
}

// replayHistory — проигрывает testdata/history.jsonl текущим Tank.Apply
// и возвращает итоговое состояние каждого стрима плюс число событий.
func replayHistory(t *testing.T) (map[string]Tank, int) {
	t.Helper()
	f, err := os.Open("testdata/history.jsonl")
	if err != nil {
		t.Fatalf("история не найдена: %v", err)
	}
	defer f.Close()

	states := map[string]Tank{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	lines := 0
	for sc.Scan() {
		var rec struct {
			Stream string          `json:"stream"`
			Type   string          `json:"type"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("строка %d не разобралась: %v", lines+1, err)
		}
		var e Event
		if err := json.Unmarshal(rec.Data, &e); err != nil {
			t.Fatalf("данные события в строке %d: %v", lines+1, err)
		}
		e.Type = rec.Type
		states[rec.Stream] = states[rec.Stream].Apply(e)
		lines++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if lines == 0 {
		t.Fatal("история пуста — тест ничего не проверяет")
	}
	return states, lines
}

// Replay-тест: прогоняем текущий код против зафиксированной реальной истории.
// Ценность не в том, что он проходит, а в том, что он ЛОВИТ регрессию —
// изменение свёртки немедленно ломает контрольные суммы.
//
// ВАЖНО (найдено на шаге 3 при доказательстве, что тест вообще на что-то
// годен): одной только проверки инварианта «остаток не уходит в минус»
// НЕДОСТАТОЧНО для истории, зафиксированной на этом стенде. Каждый стрим
// в history.jsonl засеян одним OxygenAdded на 1e9 литров (см. main.go,
// runPostgresAppend — стрим готовится ДО замера ровно так), а расход в
// зафиксированных 5000 строках исчисляется тысячами. Регрессия «Consumed
// прибавляет вместо вычитает» на ТАКОЙ истории только УСКОРЯЕТ рост
// остатка — он остаётся огромным положительным числом и НИКОГДА не
// уходит в минус, поэтому проверка одного инварианта её пропускает (это
// проверено ниже эмпирически: с внесённой регрессией эта часть теста
// проходит). Поэтому тест сверяет ещё и контрольную сумму — итоговое
// состояние каждого стрима — с golden-файлом testdata/expected.json,
// зафиксированным ОДИН РАЗ на заведомо исправном Apply. Расхождение
// суммы ловит регрессию независимо от знака результата.
// TestRecordedHistoryHasOnlyKnownEventTypes — fail-closed на стороне replay.
//
// Tank.Apply — switch БЕЗ ветки default: событие незнакомого типа проходит
// свёртку, не меняя состояния, без ошибки и без паники. Это документированное
// свойство (см. комментарий к Apply и раздел про пределы golden-сверки в
// статье 5), и менять сигнатуру чистой свёртки ради него не хочется. Но
// оставлять слепое место совсем без присмотра нельзя: проектор и SQL-сверка
// после фикс-раунда 5 отказываются работать на неизвестном типе, а replay
// молчал бы. Проверка ставится не внутрь Apply, а на границу — на сами
// данные, которыми кормят replay.
//
// Проверено падающим: если дописать в testdata/history.jsonl строку с
// event_type "OxygenLeaked", тест сообщает о неизвестном типе и падает.
func TestRecordedHistoryHasOnlyKnownEventTypes(t *testing.T) {
	known := map[string]bool{
		"TankRegistered": true,
		"OxygenAdded":    true,
		"OxygenConsumed": true,
	}

	f, err := os.Open("testdata/history.jsonl")
	if err != nil {
		t.Fatalf("история не найдена: %v", err)
	}
	defer f.Close()

	counts := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var rec struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("строка %d не разобралась: %v", line, err)
		}
		if !known[rec.Type] {
			counts[rec.Type]++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if line == 0 {
		t.Fatal("история пуста — тест ничего не проверяет")
	}
	if len(counts) > 0 {
		t.Fatalf("в истории есть события неизвестных типов: %v — Tank.Apply свернёт их "+
			"в ноль молча, а golden-сверка этого не заметит", counts)
	}
}

func TestReplayAgainstRecordedHistory(t *testing.T) {
	states, lines := replayHistory(t)

	if *update {
		golden := map[string]tankState{}
		for stream, tank := range states {
			golden[stream] = tankState{Registered: tank.Registered, Liters: tank.Liters}
		}
		b, err := json.MarshalIndent(golden, "", "  ")
		if err != nil {
			t.Fatalf("маршалинг golden: %v", err)
		}
		if err := os.WriteFile("testdata/expected.json", b, 0o644); err != nil {
			t.Fatalf("запись golden: %v", err)
		}
		t.Logf("golden обновлён: %d стримов", len(golden))
	}

	expectedRaw, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatalf("golden-файл не найден (сгенерировать: go test -run TestReplay -update): %v", err)
	}
	var expected map[string]tankState
	if err := json.Unmarshal(expectedRaw, &expected); err != nil {
		t.Fatalf("golden-файл не разобрался: %v", err)
	}
	if len(expected) != len(states) {
		t.Fatalf("число стримов разошлось: golden=%d, replay=%d", len(expected), len(states))
	}

	var mismatches []string
	for stream, want := range expected {
		got, ok := states[stream]
		if !ok {
			mismatches = append(mismatches, stream+": стрим отсутствует в replay")
			continue
		}
		if got.Registered != want.Registered || math.Abs(got.Liters-want.Liters) > 1e-9 {
			mismatches = append(mismatches, fmt.Sprintf(
				"%s: ожидали liters=%.6f registered=%v, получили liters=%.6f registered=%v",
				stream, want.Liters, want.Registered, got.Liters, got.Registered))
		}
	}
	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		t.Fatalf("контрольная сумма разошлась у %d из %d стримов (свёртка сломана):\n%s",
			len(mismatches), len(expected), joinLines(mismatches, 10))
	}

	// Инвариант домена на всей реальной истории: остаток не уходит в минус.
	// Оставлен как ВТОРАЯ, независимая проверка — но, как показано выше,
	// сам по себе он НЕ покрывает эту конкретную регрессию на этой истории.
	for stream, tank := range states {
		if tank.Liters < 0 {
			t.Fatalf("стрим %s ушёл в минус: %.3f — свёртка сломана", stream, tank.Liters)
		}
	}
	t.Logf("проиграно %d событий по %d стримам, контрольные суммы совпали с golden", lines, len(states))
}

func joinLines(lines []string, limit int) string {
	if len(lines) > limit {
		lines = append(append([]string{}, lines[:limit]...), "…")
	}
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}
