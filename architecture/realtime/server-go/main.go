// Сервер стенда realtime на Go. Тот же протокол реализует server-java.
//
// Маршруты:
//
//	GET  /sse?topic=t           поток text/event-stream; Last-Event-ID — догон из истории шины
//	GET  /ws?topic=t            WebSocket, сервер шлёт события текстовыми кадрами
//	GET  /poll?topic=t&after=N  short polling: события с id > N, сразу, возможно пусто
//	GET  /longpoll?topic=t      long polling: курсор — в памяти инстанса, по cookie sid
//	POST /publish?topic=t&id=N  событие в шину (тело — payload)
//	GET  /healthz               инстанс, сервер, состояние подписки на шину
//	GET  /debug/stats           соединения, доставлено, выброшено, отключено, макс. очередь
//	POST /debug/reset           обнулить счётчики
//	POST /debug/gc              сборка мусора и RSS процесса
//
// Переменные окружения: RT_INSTANCE, RT_BUS (none|redis|redis-streams|nats|jetstream),
// RT_REDIS, RT_NATS, RT_TOPICS, RT_HEARTBEAT_MS (0 — без heartbeat), RT_QUEUE
// (unbounded|drop-oldest|disconnect), RT_QUEUE_LEN, RT_LONGPOLL_MS, RT_SSE_RETRY_MS,
// RT_SSE_NO_BUFFER_HEADER (1 — заголовок X-Accel-Buffering: no), RT_WS_COMPRESS (1 —
// permessage-deflate), RT_WS_COMPRESS_THRESHOLD (порог сжатия WS, 0 — по умолчанию
// библиотеки), RT_SSE_GZIP (1 — gzip потока SSE, если клиент его принимает), RT_SSE_GZIP_LEVEL (уровень
// gzip, по умолчанию — уровень библиотеки), RT_HISTORY_DELAY_MS (пауза перед чтением
// истории), RT_HISTORY_FAIL (1 — чтение истории падает), RT_HISTORY_BARRIER (1 — после
// чтения истории ждать POST /debug/release: управляемый стык), RT_SUBSCRIBE_LATE (1 —
// инъекция неверного порядка: подписка после истории), RT_PORT.
package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type config struct {
	instance, busKind, queue string
	queueLen                 int
	heartbeat, longpoll      time.Duration
	sseRetry                 int
	sseNoBuffer, wsCompress  bool
	sseGzip                  bool
	wsThreshold, gzipLevel   int
	historyDelay             time.Duration
	historyFail              bool
	historyBarrier           bool
	subscribeLate            bool
}

var (
	cfg config
	hub = newHub()
	bus Bus
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v, err := strconv.Atoi(env(name, strconv.Itoa(def)))
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return v
}

func main() {
	cfg = config{
		instance:       env("RT_INSTANCE", "go-1"),
		busKind:        env("RT_BUS", "none"),
		queue:          env("RT_QUEUE", QueueUnbounded),
		queueLen:       envInt("RT_QUEUE_LEN", 256),
		heartbeat:      time.Duration(envInt("RT_HEARTBEAT_MS", 0)) * time.Millisecond,
		longpoll:       time.Duration(envInt("RT_LONGPOLL_MS", 2000)) * time.Millisecond,
		sseRetry:       envInt("RT_SSE_RETRY_MS", 1000),
		sseNoBuffer:    env("RT_SSE_NO_BUFFER_HEADER", "0") == "1",
		wsCompress:     env("RT_WS_COMPRESS", "0") == "1",
		sseGzip:        env("RT_SSE_GZIP", "0") == "1",
		wsThreshold:    envInt("RT_WS_COMPRESS_THRESHOLD", 0),
		gzipLevel:      envInt("RT_SSE_GZIP_LEVEL", gzip.DefaultCompression),
		historyDelay:   time.Duration(envInt("RT_HISTORY_DELAY_MS", 0)) * time.Millisecond,
		historyFail:    env("RT_HISTORY_FAIL", "0") == "1",
		historyBarrier: env("RT_HISTORY_BARRIER", "0") == "1",
		subscribeLate:  env("RT_SUBSCRIBE_LATE", "0") == "1",
	}
	var err error
	bus, err = newBus(cfg.busKind, env("RT_REDIS", "redis:6379"), env("RT_NATS", "nats://nats:4222"),
		strings.Split(env("RT_TOPICS", "t"), ","))
	if err != nil {
		log.Fatal(err)
	}
	bus.Run(context.Background(), hub)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", handleSSE)
	mux.HandleFunc("GET /ws", handleWS)
	mux.HandleFunc("GET /poll", handlePoll)
	mux.HandleFunc("GET /longpoll", handleLongPoll)
	mux.HandleFunc("POST /publish", handlePublish)
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /debug/stats", handleStats)
	mux.HandleFunc("POST /debug/reset", handleReset)
	mux.HandleFunc("POST /debug/gc", handleGC)
	mux.HandleFunc("GET /debug/barrier", handleBarrierState)
	mux.HandleFunc("POST /debug/release", handleBarrierRelease)
	addr := ":" + env("RT_PORT", "8080")
	log.Printf("%s: шина %s, очередь %s/%d, heartbeat %v, слушаю %s",
		cfg.instance, cfg.busKind, cfg.queue, cfg.queueLen, cfg.heartbeat, addr)
	srv := &http.Server{Addr: addr, Handler: withInstance(mux)}
	log.Fatal(srv.ListenAndServe())
}

// withInstance — заголовок X-Instance в каждом ответе: клиент знает, на какой
// инстанс попал, и стенд проверяет, что клиенты разошлись по обоим.
func withInstance(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Instance", cfg.instance)
		h.ServeHTTP(w, r)
	})
}

func topicOf(r *http.Request) string {
	if t := r.URL.Query().Get("topic"); t != "" {
		return t
	}
	return "t"
}

// --- SSE -----------------------------------------------------------------------

func handleSSE(w http.ResponseWriter, r *http.Request) {
	topic := topicOf(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "нет Flusher", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	if cfg.sseNoBuffer {
		h.Set("X-Accel-Buffering", "no")
	}
	sub := newSubscriber(cfg.queue, cfg.queueLen)
	subscribed := false
	subscribe := func() {
		hub.subscribe(topic, sub)
		subscribed = true
	}
	defer func() {
		if subscribed {
			hub.unsubscribe(topic, sub)
		}
		sub.close()
	}()
	if !cfg.subscribeLate {
		subscribe() // сначала подписка, потом история: стык без пропуска
	}

	// История — до начала ответа. Ошибка её чтения — отказ 503, а не поток:
	// живое событие после непрочитанной истории сдвинуло бы курсор клиента за
	// пропуск, и следующее переподключение его уже не вернуло бы. «Истории нет»
	// (шина без журнала) — не ошибка: поток идёт, пропуск остаётся (так задумано
	// для опыта «без истории»).
	var past []Event
	var sent int64
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		if after, err := strconv.ParseInt(last, 10, 64); err == nil {
			sent = after
			if cfg.historyDelay > 0 { // барьер опыта: живые события приходят, пока история читается
				time.Sleep(cfg.historyDelay)
			}
			past, err = readHistory(r.Context(), topic, after)
			if err != nil && !errors.Is(err, errNoHistory) {
				http.Error(w, "история недоступна: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
			if cfg.historyBarrier { // история прочитана — ждать разрешения сценария
				if !barrierWait(r.Context()) {
					return
				}
			}
		}
	}
	if cfg.subscribeLate {
		subscribe() // инъекция неверного порядка: события между историей и подпиской теряются
	}

	// gzip потока: каждый сброс — Flush сжатия, иначе события ждали бы в буфере
	// компрессора так же, как в буфере прокси.
	var out io.Writer = w
	var gz *gzip.Writer
	if cfg.sseGzip && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		gz, _ = gzip.NewWriterLevel(w, cfg.gzipLevel)
		defer gz.Close()
		out = gz
	}
	bw := bufio.NewWriter(out)
	flush := func() error {
		if err := bw.Flush(); err != nil {
			return err
		}
		if gz != nil {
			if err := gz.Flush(); err != nil {
				return err
			}
		}
		flusher.Flush()
		return nil
	}
	fmt.Fprintf(bw, "retry: %d\n\n", cfg.sseRetry)
	for _, e := range past {
		writeSSE(bw, e)
		sent = e.ID
	}
	if flush() != nil {
		return
	}

	events := make(chan []Event)
	go func() {
		defer close(events)
		for {
			batch := sub.next()
			if batch == nil {
				return
			}
			select {
			case events <- batch:
			case <-r.Context().Done():
				return
			}
		}
	}()
	var tick <-chan time.Time
	if cfg.heartbeat > 0 {
		t := time.NewTicker(cfg.heartbeat)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case batch, ok := <-events:
			if !ok {
				return // очередь закрыта: медленного клиента отключили
			}
			for _, e := range batch {
				if e.ID <= sent { // уже отдано из истории
					continue
				}
				writeSSE(bw, e)
				sent = e.ID
			}
		case <-tick:
			io.WriteString(bw, ": heartbeat\n\n") // комментарий: клиент его не видит
		case <-r.Context().Done():
			return
		}
		if flush() != nil {
			return
		}
	}
}

// barrier — управляемый стык истории и живого потока (RT_HISTORY_BARRIER=1):
// обработчик SSE, прочитав историю, сообщает «жду» и ждёт разрешения. Сценарий
// публикует в это время события — прийти к клиенту они могут только по живой
// подписке, потому что история уже прочитана.
var barrier = struct {
	sync.Mutex
	waiting bool
	release chan struct{}
}{release: make(chan struct{})}

func barrierWait(ctx context.Context) bool {
	barrier.Lock()
	barrier.waiting = true
	ch := barrier.release
	barrier.Unlock()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func handleBarrierState(w http.ResponseWriter, _ *http.Request) {
	barrier.Lock()
	defer barrier.Unlock()
	writeJSON(w, map[string]any{"waiting": barrier.waiting})
}

func handleBarrierRelease(w http.ResponseWriter, _ *http.Request) {
	barrier.Lock()
	close(barrier.release)
	barrier.release = make(chan struct{})
	barrier.waiting = false
	barrier.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// readHistory — история шины; RT_HISTORY_FAIL=1 — инъекция отказа для опыта.
func readHistory(ctx context.Context, topic string, after int64) ([]Event, error) {
	if cfg.historyFail {
		return nil, errors.New("инъекция RT_HISTORY_FAIL")
	}
	return bus.History(ctx, topic, after)
}

func writeSSE(w io.Writer, e Event) {
	data, _ := json.Marshal(e)
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, data)
}

// --- WebSocket -----------------------------------------------------------------

func handleWS(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled}
	if cfg.wsCompress {
		opts.CompressionMode = websocket.CompressionContextTakeover
		// 0 — порог библиотеки по умолчанию: при общем контексте сообщения короче
		// 128 байт не сжимаются вовсе.
		opts.CompressionThreshold = cfg.wsThreshold
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return // Accept уже ответил клиенту
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 16)
	ctx := c.CloseRead(r.Context()) // читать управляющие кадры (pong, close)

	topic := topicOf(r)
	sub := newSubscriber(cfg.queue, cfg.queueLen)
	hub.subscribe(topic, sub)
	defer hub.unsubscribe(topic, sub)
	defer sub.close()

	if cfg.heartbeat > 0 {
		go func() {
			t := time.NewTicker(cfg.heartbeat)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					pctx, cancel := context.WithTimeout(ctx, cfg.heartbeat)
					err := c.Ping(pctx)
					cancel()
					if err != nil {
						c.CloseNow()
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() { <-ctx.Done(); sub.close() }()
	for {
		batch := sub.next()
		if batch == nil {
			c.Close(websocket.StatusPolicyViolation, "очередь переполнена или соединение закрыто")
			return
		}
		for _, e := range batch {
			data, _ := json.Marshal(e)
			if err := c.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

// --- polling -------------------------------------------------------------------

func handlePoll(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	events, _ := hub.after(topicOf(r), after)
	writeJSON(w, events)
}

// Курсор long polling — в памяти инстанса, ключ — cookie sid. Без привязки
// клиента к инстансу следующий запрос попадёт на чужой курсор.
var cursors = struct {
	sync.Mutex
	m map[string]int64
}{m: map[string]int64{}}

func handleLongPoll(w http.ResponseWriter, r *http.Request) {
	topic := topicOf(r)
	sid := ""
	if c, err := r.Cookie("sid"); err == nil {
		sid = c.Value
	}
	cursors.Lock()
	cur, known := cursors.m[sid+"|"+topic]
	if !known { // новый для этого инстанса клиент начинает «с текущего момента»
		cur = hub.latest(topic)
	}
	cursors.Unlock()
	deadline := time.NewTimer(cfg.longpoll)
	defer deadline.Stop()
	for {
		events, wait := hub.after(topic, cur)
		if len(events) > 0 {
			cursors.Lock()
			cursors.m[sid+"|"+topic] = events[len(events)-1].ID
			cursors.Unlock()
			writeJSON(w, events)
			return
		}
		select {
		case <-wait:
		case <-deadline.C:
			cursors.Lock()
			cursors.m[sid+"|"+topic] = cur
			cursors.Unlock()
			writeJSON(w, []Event{})
			return
		case <-r.Context().Done():
			return
		}
	}
}

// --- публикация и служебное ----------------------------------------------------

func handlePublish(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "id", http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := bus.Publish(r.Context(), Event{ID: id, Topic: topicOf(r), Payload: string(body)}); err != nil {
		http.Error(w, "шина: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	state := "down"
	if bus.Up() {
		state = "up"
	}
	writeJSON(w, map[string]any{"instance": cfg.instance, "server": "go", "bus": cfg.busKind,
		"bus_state": state, "conns": stats.conns.Load()})
}

func handleStats(w http.ResponseWriter, _ *http.Request) {
	liveMax, liveDropped := hub.liveMaxQueue()
	maxQ := stats.maxQueue.Load()
	if int64(liveMax) > maxQ {
		maxQ = int64(liveMax)
	}
	writeJSON(w, map[string]any{"instance": cfg.instance, "conns": stats.conns.Load(),
		"delivered": stats.delivered.Load(), "dropped": stats.dropped.Load() + liveDropped,
		"disconnected": stats.disconnected.Load(), "max_queue": maxQ})
}

func handleReset(w http.ResponseWriter, _ *http.Request) {
	stats.delivered.Store(0)
	stats.dropped.Store(0)
	stats.disconnected.Store(0)
	stats.maxQueue.Store(0)
	w.WriteHeader(http.StatusNoContent)
}

func handleGC(w http.ResponseWriter, _ *http.Request) {
	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	writeJSON(w, map[string]any{"instance": cfg.instance, "rss_kb": rssKB(), "heap_kb": ms.HeapInuse / 1024,
		"goroutines": runtime.NumGoroutine(), "conns": stats.conns.Load()})
}

// rssKB — VmRSS процесса из /proc: так же его читает server-java.
func rssKB() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			v, _ := strconv.ParseInt(strings.Fields(line)[1], 10, 64)
			return v
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
