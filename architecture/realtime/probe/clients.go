package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type Event struct {
	ID      int64  `json:"id"`
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}

// Received — что получил один клиент: номера событий по порядку прихода.
type Received struct {
	mu       sync.Mutex
	IDs      []int64
	Instance string
	Closed   string // чем кончилось соединение: "" — живо
	Requests int
	Empty    int
}

func (r *Received) add(id int64) {
	r.mu.Lock()
	r.IDs = append(r.IDs, id)
	r.mu.Unlock()
}

func (r *Received) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.IDs)
}

func (r *Received) snapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.IDs...)
}

func (r *Received) last() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.IDs) == 0 {
		return 0
	}
	return r.IDs[len(r.IDs)-1]
}

// dialer с маленьким приёмным буфером — для медленного клиента: иначе события
// копятся в ядре клиента, а не в очереди сервера.
func smallBufDialer(rcvbuf int) *net.Dialer {
	return &net.Dialer{Timeout: 3 * time.Second, Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		c.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, rcvbuf)
		})
		return err
	}}
}

func httpClient(rcvbuf int) *http.Client {
	tr := &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 4}
	if rcvbuf > 0 {
		tr.DialContext = smallBufDialer(rcvbuf).DialContext
	}
	return &http.Client{Transport: tr}
}

// --- SSE: поднабор, нужный стенду ------------------------------------------------
// Не браузерный EventSource: числовые id, поле retry не обрабатывается, строки
// data: склеиваются без перевода строки, переподключение задаёт сценарий опыта.

type sseOpts struct {
	url, topic string
	lastID     int64 // 0 — без Last-Event-ID
	readDelay  time.Duration
	rcvbuf     int
	gzip       bool
}

// sseOnce — одно соединение SSE до закрытия или отмены контекста. Возвращает
// последний id (для переподключения) и retry из потока.
func sseOnce(ctx context.Context, o sseOpts, rec *Received) (int64, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", o.url+"/sse?topic="+o.topic, nil)
	req.Header.Set("Accept", "text/event-stream")
	if o.lastID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(o.lastID, 10))
	}
	if o.gzip {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	resp, err := httpClient(o.rcvbuf).Do(req)
	if err != nil {
		return o.lastID, err
	}
	defer resp.Body.Close()
	rec.mu.Lock()
	rec.Instance = resp.Header.Get("X-Instance")
	rec.Requests++
	rec.mu.Unlock()
	if resp.StatusCode != 200 {
		return o.lastID, fmt.Errorf("статус %d", resp.StatusCode)
	}
	last := o.lastID
	var body io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return last, err
		}
		body = zr
	}
	br := bufio.NewReaderSize(body, 512)
	var data strings.Builder
	var id int64 = -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return last, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if data.Len() > 0 {
				var e Event
				if json.Unmarshal([]byte(data.String()), &e) == nil {
					rec.add(e.ID)
				}
				if id >= 0 {
					last = id
				}
				if o.readDelay > 0 {
					time.Sleep(o.readDelay)
				}
			}
			data.Reset()
			id = -1
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id:"):
			id, _ = strconv.ParseInt(strings.TrimSpace(line[3:]), 10, 64)
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(line[5:], " "))
		}
	}
}

// --- WebSocket ------------------------------------------------------------------

type wsOpts struct {
	url, topic string
	readDelay  time.Duration
	rcvbuf     int
	compress   bool
}

func wsURL(base string) string {
	return "ws" + strings.TrimPrefix(base, "http")
}

// wsOnce — одно соединение до закрытия. Ошибка несёт код закрытия, если он был.
func wsOnce(ctx context.Context, o wsOpts, rec *Received) error {
	opts := &websocket.DialOptions{HTTPClient: httpClient(o.rcvbuf),
		CompressionMode: websocket.CompressionDisabled}
	if o.compress {
		opts.CompressionMode = websocket.CompressionContextTakeover
	}
	c, resp, err := websocket.Dial(ctx, wsURL(o.url)+"/ws?topic="+o.topic, opts)
	if resp != nil {
		rec.mu.Lock()
		rec.Instance = resp.Header.Get("X-Instance")
		rec.Requests++
		rec.mu.Unlock()
	}
	if err != nil {
		if resp != nil {
			return fmt.Errorf("рукопожатие: статус %d", resp.StatusCode)
		}
		return fmt.Errorf("рукопожатие: %w", err)
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var e Event
		if json.Unmarshal(data, &e) == nil {
			rec.add(e.ID)
		}
		if o.readDelay > 0 {
			time.Sleep(o.readDelay)
		}
	}
}

// closeReason — как кончилось соединение, словами стенда.
func closeReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	if s := websocket.CloseStatus(err); s != -1 {
		return "close " + strconv.Itoa(int(s))
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection reset"):
		return "reset"
	case strings.Contains(msg, "EOF"):
		return "eof"
	case strings.HasPrefix(msg, "рукопожатие"):
		return msg
	}
	return "error: " + msg
}

// --- polling ---------------------------------------------------------------------

func pollOnce(ctx context.Context, cl *http.Client, base, topic string, after int64, rec *Received) (int64, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/poll?topic=%s&after=%d", base, topic, after), nil)
	resp, err := cl.Do(req)
	if err != nil {
		return after, err
	}
	defer resp.Body.Close()
	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return after, err
	}
	rec.mu.Lock()
	rec.Requests++
	if len(events) == 0 {
		rec.Empty++
	}
	rec.Instance = resp.Header.Get("X-Instance")
	rec.mu.Unlock()
	for _, e := range events {
		rec.add(e.ID)
		after = e.ID
	}
	return after, nil
}

func longpollClient(sid string) *http.Client {
	jar, _ := cookiejar.New(nil)
	cl := httpClient(0)
	cl.Jar = jar
	return cl
}

func longpollOnce(ctx context.Context, cl *http.Client, base, topic, sid string, rec *Received) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/longpoll?topic="+topic, nil)
	req.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return "", err
	}
	rec.mu.Lock()
	rec.Requests++
	if len(events) == 0 {
		rec.Empty++
	}
	rec.mu.Unlock()
	for _, e := range events {
		rec.add(e.ID)
	}
	return resp.Header.Get("X-Instance"), nil
}

// --- издатель и служебные запросы ------------------------------------------------

func publish(base, topic string, id int64, payload string) (string, error) {
	resp, err := http.Post(fmt.Sprintf("%s/publish?topic=%s&id=%d", base, topic, id),
		"text/plain", strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("publish %d: статус %d", id, resp.StatusCode)
	}
	return resp.Header.Get("X-Instance"), nil
}

func getJSON(url string, v any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func postJSON(url string, v any) error {
	resp, err := http.Post(url, "text/plain", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if v == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func payloadOf(size int, id int64) string {
	s := fmt.Sprintf("p%d-", id)
	if len(s) >= size {
		return s[:size]
	}
	return s + strings.Repeat("x", size-len(s))
}

// gapsAndDups — сколько номеров из 1..n не пришло и сколько пришло повторно.
func gapsAndDups(ids []int64, from, to int64) (missing []int64, dups int) {
	seen := map[int64]int{}
	for _, id := range ids {
		seen[id]++
	}
	for id := from; id <= to; id++ {
		if seen[id] == 0 {
			missing = append(missing, id)
		}
	}
	for _, c := range seen {
		if c > 1 {
			dups += c - 1
		}
	}
	return
}
