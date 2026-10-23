package main

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// check — один и тот же набор запросов к серверу; итог — наблюдения без имён
// инстансов. Проверка паритета сравнивает итоги Go и Java: расхождение —
// находка про сервер, а не шум стенда.
func check(url string) map[string]any {
	obs := map[string]any{}
	keys := func(path string) []string {
		var m map[string]any
		if err := getJSON(url+path, &m); err != nil {
			return []string{"error: " + err.Error()}
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	obs["healthz_keys"] = keys("/healthz")
	obs["stats_keys"] = keys("/debug/stats")
	status := func(method, path string) int {
		req, _ := http.NewRequest(method, url+path, strings.NewReader("x"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return -1
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	obs["publish_without_id"] = status("POST", "/publish?topic=pc")
	obs["publish_ok"] = status("POST", "/publish?topic=pc&id=1")
	poll := func(after string) []int64 {
		rec := &Received{}
		var a int64
		if after == "1" {
			a = 1
		}
		pollOnce(context.Background(), httpClient(0), url, "pc", a, rec)
		return rec.snapshot()
	}
	obs["poll_after_0"] = poll("0")
	obs["poll_after_1"] = poll("1")

	// SSE: заголовки, живые события, догон без истории.
	ctx, cancel := context.WithCancel(context.Background())
	sse := &Received{}
	var last int64
	done := make(chan struct{})
	go func() { last, _ = sseOnce(ctx, sseOpts{url: url, topic: "ps"}, sse); close(done) }()
	time.Sleep(500 * time.Millisecond)
	publishRange(url, "ps", 1, 3, 8, 0)
	waitFor(3*time.Second, func() bool { return sse.count() >= 3 })
	cancel()
	<-done
	obs["sse_ids"] = sse.snapshot()
	obs["sse_instance_header"] = sse.Instance != ""
	obs["sse_last_event_id"] = last
	publishRange(url, "ps", 4, 4, 8, 0)
	resumed := &Received{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { sseOnce(ctx2, sseOpts{url: url, topic: "ps", lastID: last}, resumed); close(done2) }()
	time.Sleep(500 * time.Millisecond)
	publishRange(url, "ps", 5, 5, 8, 0)
	waitFor(3*time.Second, func() bool { return resumed.count() >= 1 })
	cancel2()
	<-done2
	obs["sse_resume_without_history"] = resumed.snapshot()

	// Заголовки ответа SSE.
	req, _ := http.NewRequest("GET", url+"/sse?topic=ph", nil)
	ctx3, cancel3 := context.WithTimeout(context.Background(), time.Second)
	resp, err := httpClient(0).Do(req.WithContext(ctx3))
	if err == nil {
		obs["sse_content_type"] = strings.Split(resp.Header.Get("Content-Type"), ";")[0]
		obs["sse_cache_control"] = resp.Header.Get("Cache-Control")
		resp.Body.Close()
	}
	cancel3()

	// WebSocket.
	ws := &Received{}
	ctx4, cancel4 := context.WithCancel(context.Background())
	done4 := make(chan struct{})
	go func() { wsOnce(ctx4, wsOpts{url: url, topic: "pw"}, ws); close(done4) }()
	time.Sleep(500 * time.Millisecond)
	publishRange(url, "pw", 1, 3, 8, 0)
	waitFor(3*time.Second, func() bool { return ws.count() >= 3 })
	cancel4()
	<-done4
	obs["ws_ids"] = ws.snapshot()
	obs["ws_instance_header"] = ws.Instance != ""

	// Long polling: новый клиент начинает «с текущего момента».
	publishRange(url, "pl", 1, 2, 8, 0)
	lp := &Received{}
	cl := longpollClient("s1")
	got := make(chan struct{})
	go func() { longpollOnce(context.Background(), cl, url, "pl", "s1", lp); close(got) }()
	time.Sleep(300 * time.Millisecond)
	publishRange(url, "pl", 3, 3, 8, 0)
	<-got
	longpollOnce(context.Background(), cl, url, "pl", "s1", lp) // пусто, по таймауту
	obs["longpoll_ids"] = lp.snapshot()
	obs["longpoll_requests"] = lp.Requests
	obs["longpoll_empty"] = lp.Empty
	return obs
}
