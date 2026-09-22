package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

var (
	clients  = []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "172.17.0.5", "192.168.1.7"}
	users    = []string{"alice", "bob", "carol", "-"}
	methods  = []string{"GET", "GET", "GET", "POST", "PUT", "DELETE"}
	paths    = []string{"/", "/api/items", "/api/items/7", "/health", "/api/orders", "/static/app.js"}
	statuses = []int{200, 200, 200, 200, 301, 404, 500}
	agents   = []string{"curl/8.5.0", "Mozilla/5.0", "Go-http-client/2.0"}
	levels   = []string{"info", "info", "info", "warn", "error", "debug"}
	services = []string{"api", "worker", "auth"}
	messages = []string{"request handled", "cache miss", "retry scheduled", "db query slow"}
)

// NginxLine возвращает строку access-лога nginx в combined-формате.
func NginxLine(r *rand.Rand, ts time.Time) string {
	return fmt.Sprintf(`%s - %s [%s] "%s %s HTTP/1.1" %d %d "%s" "%s"`,
		clients[r.Intn(len(clients))],
		users[r.Intn(len(users))],
		ts.Format("02/Jan/2006:15:04:05 -0700"),
		methods[r.Intn(len(methods))],
		paths[r.Intn(len(paths))],
		statuses[r.Intn(len(statuses))],
		100+r.Intn(9900),
		"-",
		agents[r.Intn(len(agents))],
	)
}

// AppJSONLine возвращает строку структурного лога приложения.
// Набор значений намеренно узкий: часть событий повторяется, чтобы dedupe в агенте
// имел что отбрасывать.
func AppJSONLine(r *rand.Rand, ts time.Time) string {
	return fmt.Sprintf(
		`{"ts":"%s","level":"%s","service":"%s","msg":"%s","duration_ms":%d}`,
		ts.UTC().Format(time.RFC3339),
		levels[r.Intn(len(levels))],
		services[r.Intn(len(services))],
		messages[r.Intn(len(messages))],
		r.Intn(50),
	)
}

// appDedupeKey отбрасывает поле ts и возвращает остаток строки — ровно те поля,
// по которым настроен dedupe в агенте (service, level, msg, duration_ms).
// Совпадение ключей означает, что дедупликация отбросит второе событие.
func appDedupeKey(line string) string {
	if i := strings.Index(line, ","); i >= 0 {
		return line[i+1:]
	}
	return line
}
