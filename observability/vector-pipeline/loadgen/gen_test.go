package main

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
)

// combined-формат nginx: client - user [ts] "request" status size "referer" "agent"
var nginxCombined = regexp.MustCompile(
	`^\d+\.\d+\.\d+\.\d+ - \S+ \[\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}\] ` +
		`"(GET|POST|PUT|DELETE) \S+ HTTP/1\.1" \d{3} \d+ "[^"]*" "[^"]*"$`)

func TestNginxLineMatchesCombinedFormat(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)

	for i := 0; i < 200; i++ {
		line := NginxLine(r, ts)
		if !nginxCombined.MatchString(line) {
			t.Fatalf("строка не в combined-формате: %q", line)
		}
	}
}

func TestAppJSONLineIsValidJSONWithRequiredFields(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)

	var ev map[string]any
	if err := json.Unmarshal([]byte(AppJSONLine(r, ts)), &ev); err != nil {
		t.Fatalf("невалидный JSON: %v", err)
	}
	for _, f := range []string{"level", "service", "msg", "ts", "duration_ms"} {
		if _, ok := ev[f]; !ok {
			t.Fatalf("нет обязательного поля %q в %v", f, ev)
		}
	}
}

func TestSameSeedGivesSameOutput(t *testing.T) {
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)
	a := NginxLine(rand.New(rand.NewSource(7)), ts)
	b := NginxLine(rand.New(rand.NewSource(7)), ts)
	if a != b {
		t.Fatalf("генератор не воспроизводим: %q != %q", a, b)
	}
}

func TestAppJSONLineProducesDuplicatesForDedupeTest(t *testing.T) {
	// dedupe в агенте должен иметь что дедуплицировать: часть событий обязана повторяться
	r := rand.New(rand.NewSource(1))
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)

	seen := map[string]int{}
	for i := 0; i < 5000; i++ {
		seen[AppJSONLine(r, ts)]++
	}
	dups := 0
	for _, n := range seen {
		if n > 1 {
			dups += n - 1
		}
	}
	if dups == 0 {
		t.Fatal("генератор не даёт повторов — замер dedupe будет бессмысленным")
	}
}

func TestEmitTickFeedsBothFilesWhenPerTickIsOne(t *testing.T) {
	// perTick == 1 — это любой rate меньше 200. Если чередование считать по
	// позиции внутри тика, app.log не получит ни строки за весь прогон.
	var nginx, app bytes.Buffer
	r := rand.New(rand.NewSource(1))
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)

	var m manifest
	seen := map[string]struct{}{}
	for tick := 0; tick < 10; tick++ {
		m = emitTick(&nginx, &app, r, ts, 1, m, seen)
	}

	if m.Nginx == 0 || m.App == 0 {
		t.Fatalf("при perTick=1 один из файлов остался пустым: nginx=%d app=%d", m.Nginx, m.App)
	}
	if m.Nginx+m.App != 10 {
		t.Fatalf("записано не 10 строк: nginx=%d app=%d", m.Nginx, m.App)
	}
	if app.Len() == 0 {
		t.Fatal("буфер app.log пуст, хотя счётчик ненулевой")
	}
}

func TestNginxExpectedCountsOnlyNonHealthLines(t *testing.T) {
	var nginx, app bytes.Buffer
	r := rand.New(rand.NewSource(3))
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)

	var m manifest
	seen := map[string]struct{}{}
	for tick := 0; tick < 2000; tick++ {
		m = emitTick(&nginx, &app, r, ts, 1, m, seen)
	}

	health := 0
	for _, line := range strings.Split(strings.TrimRight(nginx.String(), "\n"), "\n") {
		if strings.Contains(line, "/health") {
			health++
		}
	}
	if health == 0 {
		t.Fatal("в выборке не оказалось запросов к /health — проверка бессмысленна")
	}
	if m.NginxExpected != m.Nginx-int64(health) {
		t.Fatalf("nginx_expected=%d, а должно быть %d (всего %d, из них /health %d)",
			m.NginxExpected, m.Nginx-int64(health), m.Nginx, health)
	}
}

func TestAppExpectedCountsDistinctDedupeKeys(t *testing.T) {
	var nginx, app bytes.Buffer
	r := rand.New(rand.NewSource(4))
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)

	var m manifest
	seen := map[string]struct{}{}
	for tick := 0; tick < 4000; tick++ {
		m = emitTick(&nginx, &app, r, ts, 1, m, seen)
	}

	uniq := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimRight(app.String(), "\n"), "\n") {
		uniq[appDedupeKey(line)] = struct{}{}
	}
	if int64(len(uniq)) != m.AppExpected {
		t.Fatalf("app_expected=%d, а различимых ключей %d", m.AppExpected, len(uniq))
	}
	if m.AppExpected >= m.App {
		t.Fatalf("повторов не оказалось вовсе (app=%d, различимых=%d) — дедупу нечего отбрасывать",
			m.App, m.AppExpected)
	}
}

func TestAppJSONLineSameSeedGivesSameOutput(t *testing.T) {
	ts := time.Date(2026, 7, 27, 10, 15, 1, 0, time.UTC)
	a := AppJSONLine(rand.New(rand.NewSource(7)), ts)
	b := AppJSONLine(rand.New(rand.NewSource(7)), ts)
	if a != b {
		t.Fatalf("генератор JSON-строк не воспроизводим: %q != %q", a, b)
	}
}
