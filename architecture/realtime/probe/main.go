// Клиент-проба стенда realtime. Каждая подкоманда — один сценарий; итог —
// одна строка JSON на stdout. Python-раннеры поднимают контейнеры, зовут пробу
// и пишут фикстуры.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: proxy|check|wire|cancel-resume|sse-window|ws-handshake|idle|fanout|longpoll|gap|sse-resume|storm|slow|hold")
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var (
		url       = fs.String("url", "http://server:8080", "куда подключаются клиенты")
		pub       = fs.String("publish", "", "куда публиковать (по умолчанию -url)")
		topic     = fs.String("topic", "t", "тема")
		transport = fs.String("transport", "sse", "sse|ws|poll|longpoll")
		n         = fs.Int("n", 10, "событий")
		nBefore   = fs.Int("n-before", 5, "событий до разрыва")
		nDuring   = fs.Int("n-during", 5, "событий во время разрыва")
		nAfter    = fs.Int("n-after", 5, "событий после разрыва")
		clients   = fs.Int("clients", 10, "клиентов")
		payload   = fs.Int("payload", 64, "байт полезной нагрузки")
		evEvery   = fs.Duration("event-interval", 50*time.Millisecond, "интервал между событиями")
		pollEvery = fs.Duration("poll-interval", 200*time.Millisecond, "интервал опроса")
		window    = fs.Duration("window", 2*time.Second, "окно счёта")
		idle      = fs.Duration("idle", 6*time.Second, "простой")
		settle    = fs.Duration("settle", 500*time.Millisecond, "пауза на установление соединений")
		wait      = fs.Duration("wait", 10*time.Second, "предел ожидания")
		control   = fs.String("control", "", "управление прокси (stats/reset/cut/restore)")
		health    = fs.String("health", "", "/healthz инстанса-подписчика")
		jitter    = fs.Duration("jitter", 0, "джиттер переподключения (0 — без)")
		backoff   = fs.Duration("backoff", 200*time.Millisecond, "базовая пауза переподключения")
		readDelay = fs.Duration("read-delay", 0, "пауза после каждого события (медленный клиент)")
		rcvbuf    = fs.Int("rcvbuf", 0, "SO_RCVBUF клиента (0 — по умолчанию)")
		compress  = fs.Bool("compress", false, "сжатие: permessage-deflate (ws) или gzip (sse)")
		listen    = fs.String("listen", ":9000", "proxy: где слушать")
		target    = fs.String("target", "", "proxy: куда пересылать")
		ctlListen = fs.String("control-listen", ":9100", "proxy: управление")
		seed      = fs.Int64("seed", 1, "seed джиттера")
		overlap   = fs.Bool("overlap", false, "sse-resume: публиковать, пока сервер стоит на барьере после истории")
	)
	fs.Parse(args)
	if *pub == "" {
		*pub = *url
	}
	var out any
	switch cmd {
	case "proxy":
		runProxy(*listen, *target, *ctlListen)
		return
	case "check":
		out = check(*url)
	case "wire":
		out = wire(*transport, *url, *pub, *control, *topic, *n, *payload, *evEvery, *pollEvery, *wait, *compress)
	case "sse-window":
		out = sseWindow(*url, *pub, *topic, *n, *payload, *settle, *window)
	case "ws-handshake":
		rec := &Received{}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := wsOnce(ctx, wsOpts{url: *url, topic: *topic}, rec)
		cancel()
		r := closeReason(err)
		// X-Instance приходит и в ответе с ошибкой — успех решает итог рукопожатия.
		out = map[string]any{"scenario": "ws-handshake", "handshake_ok": !strings.HasPrefix(r, "рукопожатие"),
			"result": r, "instance": rec.Instance}
	case "idle":
		out = idleScenario(*transport, *url, *pub, *topic, *idle, *settle)
	case "fanout":
		out = fanout(*transport, *url, *pub, *topic, *clients, *n, *payload, *settle, *wait)
	case "longpoll":
		out = longpollScale(*url, *pub, *topic, *clients, *n, *evEvery, *wait)
	case "gap":
		out = gap(*transport, *url, *pub, *health, *control, *topic, *nBefore, *nDuring, *nAfter, *wait)
	case "sse-resume":
		out = sseResume(*url, *pub, *topic, *nBefore, *nDuring, *nAfter, *overlap, *wait)
	case "cancel-resume":
		out = cancelResume(*url, *topic, *wait)
	case "storm":
		out = storm(*url, *topic, *clients, *backoff, *jitter, *seed, *wait)
	case "slow":
		out = slow(*transport, *url, *pub, *topic, *n, *payload, *readDelay, *rcvbuf, *wait)
	case "hold":
		out = hold(*transport, *url, *topic, *clients)
	default:
		log.Fatalf("неизвестная подкоманда %q", cmd)
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

// runClient — клиент нужного транспорта до отмены контекста; итог — в rec.
func runClient(ctx context.Context, transport, url, topic string, rec *Received, pollEvery time.Duration,
	compress bool, readDelay time.Duration, rcvbuf int) {
	var err error
	switch transport {
	case "sse":
		_, err = sseOnce(ctx, sseOpts{url: url, topic: topic, gzip: compress, readDelay: readDelay, rcvbuf: rcvbuf}, rec)
	case "ws":
		err = wsOnce(ctx, wsOpts{url: url, topic: topic, compress: compress, readDelay: readDelay, rcvbuf: rcvbuf}, rec)
	case "poll":
		cl := httpClient(0)
		var after int64
		t := time.NewTicker(pollEvery)
		defer t.Stop()
		for {
			if after, err = pollOnce(ctx, cl, url, topic, after, rec); err != nil {
				break
			}
			select {
			case <-t.C:
				continue
			case <-ctx.Done():
			}
			break
		}
	case "longpoll":
		cl := longpollClient("w")
		for ctx.Err() == nil {
			if _, err = longpollOnce(ctx, cl, url, topic, "w", rec); err != nil {
				break
			}
		}
	}
	rec.mu.Lock()
	rec.Closed = closeReason(err)
	rec.mu.Unlock()
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func publishRange(base, topic string, from, to int64, size int, every time.Duration) (instances map[string]int, errs int) {
	instances = map[string]int{}
	for id := from; id <= to; id++ {
		inst, err := publish(base, topic, id, payloadOf(size, id))
		if err != nil {
			errs++
		} else {
			instances[inst]++
		}
		if every > 0 {
			time.Sleep(every)
		}
	}
	return
}

// --- ось 1 ------------------------------------------------------------------------

func wire(transport, url, pub, control, topic string, n, size int, evEvery, pollEvery, wait time.Duration,
	compress bool) map[string]any {
	postJSON(control+"/reset", nil)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &Received{}
	done := make(chan struct{})
	go func() { runClient(ctx, transport, url, topic, rec, pollEvery, compress, 0, 0); close(done) }()
	time.Sleep(500 * time.Millisecond)
	_, errs := publishRange(pub, topic, 1, int64(n), size, evEvery)
	all := waitFor(wait, func() bool { return rec.count() >= n })
	cancel()
	<-done
	time.Sleep(300 * time.Millisecond)
	var st map[string]any
	getJSON(control+"/stats", &st)
	missing, dups := gapsAndDups(rec.snapshot(), 1, int64(n))
	return map[string]any{"scenario": "wire", "transport": transport, "compress": compress, "n": n,
		"payload": size, "event_interval_ms": evEvery.Milliseconds(), "poll_interval_ms": pollEvery.Milliseconds(),
		"received": rec.count(), "all_received": all, "missing": len(missing), "dups": dups,
		"publish_errors": errs, "requests": rec.Requests, "empty": rec.Empty,
		"bytes_up": st["bytes_up"], "bytes_down": st["bytes_down"], "conns": st["conns"]}
}

// --- ось 2 ------------------------------------------------------------------------

func sseWindow(url, pub, topic string, n, size int, settle, window time.Duration) map[string]any {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &Received{}
	done := make(chan struct{})
	go func() { runClient(ctx, "sse", url, topic, rec, 0, false, 0, 0); close(done) }()
	time.Sleep(settle) // заголовки ответа при буферизации прокси могут не прийти вовсе
	_, errs := publishRange(pub, topic, 1, int64(n), size, 0)
	time.Sleep(window)
	inWindow := rec.count()
	open := rec.Closed == ""
	cancel()
	<-done
	return map[string]any{"scenario": "sse-window", "n": n, "window_ms": window.Milliseconds(),
		"received_in_window": inWindow, "stream_open_at_count": open, "publish_errors": errs}
}

func idleScenario(transport, url, pub, topic string, idle, settle time.Duration) map[string]any {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &Received{}
	done := make(chan struct{})
	go func() { runClient(ctx, transport, url, topic, rec, 0, false, 0, 0); close(done) }()
	time.Sleep(settle)
	time.Sleep(idle)
	closedDuringIdle := false
	select {
	case <-done:
		closedDuringIdle = true
	default:
	}
	_, errs := publishRange(pub, topic, 1, 1, 16, 0)
	got := waitFor(time.Second, func() bool { return rec.count() >= 1 })
	cancel()
	<-done
	return map[string]any{"scenario": "idle", "transport": transport, "idle_ms": idle.Milliseconds(),
		"closed_during_idle": closedDuringIdle, "closed_by": rec.Closed, "event_after_idle": got,
		"publish_errors": errs}
}

// --- ось 3 ------------------------------------------------------------------------

func fanout(transport, url, pub, topic string, k, n, size int, settle, wait time.Duration) map[string]any {
	ctx, cancel := context.WithCancel(context.Background())
	recs := make([]*Received, k)
	var wg sync.WaitGroup
	for i := range recs {
		recs[i] = &Received{}
		wg.Add(1)
		go func(r *Received) { defer wg.Done(); runClient(ctx, transport, url, topic, r, 0, false, 0, 0) }(recs[i])
	}
	connected := waitFor(wait, func() bool {
		for _, r := range recs {
			r.mu.Lock()
			ok := r.Instance != ""
			r.mu.Unlock()
			if !ok {
				return false
			}
		}
		return true
	})
	time.Sleep(settle)
	pubInst, errs := publishRange(pub, topic, 1, int64(n), size, 20*time.Millisecond)
	waitFor(wait, func() bool {
		for _, r := range recs {
			if r.count() < n {
				return false
			}
		}
		return true
	})
	time.Sleep(500 * time.Millisecond) // поздние события тоже засчитать
	cancel()
	wg.Wait()
	perInst := map[string]int{}
	full, none, partial := 0, 0, 0
	fullByInst := map[string]int{}
	for _, r := range recs {
		perInst[r.Instance]++
		switch c := r.count(); {
		case c >= n:
			full++
			fullByInst[r.Instance]++
		case c == 0:
			none++
		default:
			partial++
		}
	}
	return map[string]any{"scenario": "fanout", "transport": transport, "clients": k, "n": n,
		"all_connected": connected, "clients_per_instance": perInst, "publish_instances": pubInst,
		"publish_errors": errs, "clients_full": full, "clients_partial": partial, "clients_none": none,
		"full_per_instance": fullByInst}
}

func longpollScale(url, pub, topic string, k, n int, evEvery, wait time.Duration) map[string]any {
	ctx, cancel := context.WithCancel(context.Background())
	type cl struct {
		rec  *Received
		inst map[string]int
	}
	cls := make([]*cl, k)
	var wg sync.WaitGroup
	for i := range cls {
		cls[i] = &cl{rec: &Received{}, inst: map[string]int{}}
		wg.Add(1)
		go func(c *cl, sid string) {
			defer wg.Done()
			hc := longpollClient(sid)
			for ctx.Err() == nil {
				inst, err := longpollOnce(ctx, hc, url, topic, sid, c.rec)
				if err == nil {
					c.rec.mu.Lock()
					c.inst[inst]++
					c.rec.mu.Unlock()
				}
			}
		}(cls[i], fmt.Sprintf("client-%03d", i))
	}
	time.Sleep(time.Second) // каждый клиент успел встать в ожидание
	_, errs := publishRange(pub, topic, 1, int64(n), 16, evEvery)
	time.Sleep(wait)
	cancel()
	wg.Wait()
	totalMissing, totalDups, clean, multi := 0, 0, 0, 0
	for _, c := range cls {
		m, d := gapsAndDups(c.rec.snapshot(), 1, int64(n))
		totalMissing += len(m)
		totalDups += d
		if len(m) == 0 && d == 0 {
			clean++
		}
		if len(c.inst) > 1 {
			multi++
		}
	}
	return map[string]any{"scenario": "longpoll", "clients": k, "n": n, "publish_errors": errs,
		"missing_total": totalMissing, "dups_total": totalDups, "clients_clean": clean,
		"clients_on_several_instances": multi}
}

// --- ось 4 ------------------------------------------------------------------------

func busState(health string) string {
	var h map[string]any
	if err := getJSON(health, &h); err != nil {
		return "unknown"
	}
	s, _ := h["bus_state"].(string)
	return s
}

func gap(transport, url, pub, health, control, topic string, nb, nd, na int, wait time.Duration) map[string]any {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &Received{}
	done := make(chan struct{})
	go func() { runClient(ctx, transport, url, topic, rec, 0, false, 0, 0); close(done) }()
	time.Sleep(500 * time.Millisecond)
	_, e1 := publishRange(pub, topic, 1, int64(nb), 16, 20*time.Millisecond)
	before := waitFor(wait, func() bool { return rec.count() >= nb })
	postJSON(control+"/cut", nil)
	downSeen := waitFor(wait, func() bool { return busState(health) == "down" })
	_, e2 := publishRange(pub, topic, int64(nb+1), int64(nb+nd), 16, 20*time.Millisecond)
	downAfter := busState(health) == "down"
	postJSON(control+"/restore", nil)
	upAgain := waitFor(wait, func() bool { return busState(health) == "up" })
	time.Sleep(time.Second) // журнал успевает дочитать
	total := nb + nd + na
	_, e3 := publishRange(pub, topic, int64(nb+nd+1), int64(total), 16, 20*time.Millisecond)
	waitFor(wait, func() bool { return rec.last() >= int64(total) })
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	missing, dups := gapsAndDups(rec.snapshot(), 1, int64(total))
	lostDuring := 0
	for _, id := range missing {
		if id > int64(nb) && id <= int64(nb+nd) {
			lostDuring++
		}
	}
	return map[string]any{"scenario": "gap", "transport": transport, "n_before": nb, "n_during": nd,
		"n_after": na, "received_before_cut": before, "bus_down_before_publish": downSeen,
		"bus_down_after_publish": downAfter, "bus_up_again": upAgain,
		"publish_errors": e1 + e2 + e3, "missing": len(missing), "missing_during_cut": lostDuring, "dups": dups}
}

func sseResume(url, pub, topic string, nb, nd, na int, overlap bool, wait time.Duration) map[string]any {
	rec := &Received{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var last int64
	go func() { last, _ = sseOnce(ctx, sseOpts{url: url, topic: topic}, rec); close(done) }()
	time.Sleep(500 * time.Millisecond)
	_, e1 := publishRange(pub, topic, 1, int64(nb), 16, 20*time.Millisecond)
	waitFor(wait, func() bool { return rec.count() >= nb })
	cancel()
	<-done
	_, e2 := publishRange(pub, topic, int64(nb+1), int64(nb+nd), 16, 20*time.Millisecond)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	var resumeErr error
	go func() {
		_, resumeErr = sseOnce(ctx2, sseOpts{url: url, topic: topic, lastID: last}, rec)
		close(done2)
	}()
	// barrier: сервер (RT_HISTORY_BARRIER=1) прочитал историю и ждёт; события
	// «после» публикуются именно сейчас — прийти они могут только по живой
	// подписке. Затем барьер отпускается.
	barrierSeen := false
	if overlap {
		barrierSeen = waitFor(wait, func() bool {
			var b map[string]any
			return getJSON(url+"/debug/barrier", &b) == nil && b["waiting"] == true
		})
	} else {
		time.Sleep(500 * time.Millisecond)
	}
	total := nb + nd + na
	_, e3 := publishRange(pub, topic, int64(nb+nd+1), int64(total), 16, 20*time.Millisecond)
	// Контроль стыка: к концу публикации клиент не получил ничего сверх событий
	// до разрыва — сервер стоял на барьере, история ещё не отдана.
	atPublishEnd := rec.count()
	if overlap {
		time.Sleep(200 * time.Millisecond) // события дошли до очереди соединения
		postJSON(url+"/debug/release", nil)
	}
	finished := func() bool {
		select {
		case <-done2:
			return true
		default:
			return false
		}
	}
	waitFor(wait, func() bool { return rec.last() >= int64(total) || finished() })
	time.Sleep(300 * time.Millisecond)
	cancel2()
	<-done2
	missing, dups := gapsAndDups(rec.snapshot(), 1, int64(total))
	return map[string]any{"scenario": "sse-resume", "last_event_id_sent": last, "n_before": nb,
		"n_during": nd, "n_after": na, "overlap": overlap, "resume_result": closeReason(resumeErr),
		"received_at_publish_end": atPublishEnd, "barrier_seen": barrierSeen,
		"missing": len(missing), "dups": dups, "publish_errors": e1 + e2 + e3}
}

// cancelResume — клиент приходит с Last-Event-ID и уходит, пока сервер читает
// историю (RT_HISTORY_DELAY_MS); подписчик, вставший до чтения истории, обязан
// исчезнуть из хаба. Счёт — соединения сервера до, во время и после.
func cancelResume(url, topic string, wait time.Duration) map[string]any {
	conns := func() float64 {
		var st map[string]any
		getJSON(url+"/debug/stats", &st)
		c, _ := st["conns"].(float64)
		return c
	}
	before := conns()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sseOnce(ctx, sseOpts{url: url, topic: topic, lastID: 1}, &Received{}); close(done) }()
	during := 0.0
	waitFor(2*time.Second, func() bool { during = conns(); return during > before })
	cancel()
	<-done
	after := before + 1
	waitFor(wait, func() bool { after = conns(); return after == before })
	return map[string]any{"scenario": "cancel-resume", "conns_before": before, "conns_during": during,
		"conns_after": after}
}

// storm — K клиентов WS на одном инстансе; раннер перезапускает инстанс после
// READY. Каждый клиент после разрыва пытается снова: пауза backoff плюс
// случайная добавка до jitter. Счёт — попытки и удачные переподключения по
// корзинам в 100 мс от первого разрыва.
func storm(url, topic string, k int, base, jitter time.Duration, seed int64, wait time.Duration) map[string]any {
	rng := rand.New(rand.NewSource(seed))
	var mu sync.Mutex
	pauses := make([]time.Duration, 0, 1024)
	pause := func() time.Duration {
		mu.Lock()
		defer mu.Unlock()
		d := base
		if jitter > 0 {
			d += time.Duration(rng.Int63n(int64(jitter)))
		}
		pauses = append(pauses, d)
		return d
	}
	var firstClose time.Time
	attempts := map[int]int{} // корзина 100 мс → попыток
	success := map[int]int{}
	var once sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var connected, reconnected sync.WaitGroup
	connected.Add(k)
	reconnected.Add(k)
	for i := 0; i < k; i++ {
		go func() {
			first := true
			for ctx.Err() == nil {
				rec := &Received{}
				opened := make(chan struct{})
				go func() {
					waitFor(5*time.Second, func() bool {
						rec.mu.Lock()
						defer rec.mu.Unlock()
						return rec.Requests > 0 && rec.Instance != ""
					})
					close(opened)
				}()
				errc := make(chan error, 1)
				go func() { errc <- wsOnce(ctx, wsOpts{url: url, topic: topic}, rec) }()
				select {
				case <-opened:
				case <-errc:
				}
				rec.mu.Lock()
				ok := rec.Instance != ""
				rec.mu.Unlock()
				if !first {
					mu.Lock()
					b := int(time.Since(firstClose) / (100 * time.Millisecond))
					attempts[b]++
					if ok {
						success[b]++
					}
					mu.Unlock()
				}
				if ok {
					if first {
						connected.Done()
					} else {
						reconnected.Done()
						<-ctx.Done()
						return
					}
					first = false
					<-errc // соединение живёт до рестарта инстанса
					once.Do(func() { mu.Lock(); firstClose = time.Now(); mu.Unlock() })
				}
				time.Sleep(pause())
			}
		}()
	}
	connected.Wait()
	fmt.Println("READY")
	allBack := make(chan struct{})
	go func() { reconnected.Wait(); close(allBack) }()
	select {
	case <-allBack:
	case <-time.After(wait):
	}
	mu.Lock()
	defer mu.Unlock()
	maxA, maxS, totalA, totalS := 0, 0, 0, 0
	for _, v := range attempts {
		totalA += v
		if v > maxA {
			maxA = v
		}
	}
	for _, v := range success {
		totalS += v
		if v > maxS {
			maxS = v
		}
	}
	buckets := make([]int, 0, len(success))
	for b := range success {
		buckets = append(buckets, b)
	}
	sort.Ints(buckets)
	return map[string]any{"scenario": "storm", "clients": k, "backoff_ms": base.Milliseconds(),
		"jitter_ms": jitter.Milliseconds(), "reconnected": totalS, "attempts": totalA,
		"max_attempts_per_100ms": maxA, "max_reconnects_per_100ms": maxS, "buckets_with_reconnects": len(buckets)}
}

// --- ось 5 ------------------------------------------------------------------------

func slow(transport, url, pub, topic string, n, size int, readDelay time.Duration, rcvbuf int,
	wait time.Duration) map[string]any {
	postJSON(url+"/debug/reset", nil)
	ctx, cancel := context.WithCancel(context.Background())
	fast, slowR := &Received{}, &Received{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); runClient(ctx, transport, url, topic, fast, 0, false, 0, 0) }()
	go func() { defer wg.Done(); runClient(ctx, transport, url, topic, slowR, 0, false, readDelay, rcvbuf) }()
	time.Sleep(500 * time.Millisecond)
	_, errs := publishRange(pub, topic, 1, int64(n), size, 0)
	fastAll := waitFor(wait, func() bool { return fast.count() >= n })
	time.Sleep(500 * time.Millisecond)
	var st map[string]any
	getJSON(url+"/debug/stats", &st)
	slowAtPublishEnd := slowR.count()
	time.Sleep(2 * time.Second)
	slowR.mu.Lock()
	slowClosed := slowR.Closed
	slowR.mu.Unlock()
	cancel()
	wg.Wait()
	fm, _ := gapsAndDups(fast.snapshot(), 1, int64(n))
	return map[string]any{"scenario": "slow", "transport": transport, "n": n, "payload": size,
		"publish_errors": errs, "fast_received": fast.count(), "fast_all": fastAll, "fast_missing": len(fm),
		"slow_received_when_fast_done": slowAtPublishEnd, "slow_received": slowR.count(),
		"slow_closed_by": slowClosed, "server_max_queue": st["max_queue"], "server_dropped": st["dropped"],
		"server_disconnected": st["disconnected"]}
}

// --- ось 6 ------------------------------------------------------------------------

func hold(transport, url, topic string, k int) map[string]any {
	ctx, cancel := context.WithCancel(context.Background())
	recs := make([]*Received, k)
	var wg sync.WaitGroup
	for i := range recs {
		recs[i] = &Received{}
		wg.Add(1)
		go func(r *Received) { defer wg.Done(); runClient(ctx, transport, url, topic, r, 0, false, 0, 0) }(recs[i])
		if i%200 == 199 {
			time.Sleep(50 * time.Millisecond) // не устраивать шторм рукопожатий
		}
	}
	open := 0
	waitFor(60*time.Second, func() bool {
		open = 0
		for _, r := range recs {
			r.mu.Lock()
			if r.Instance != "" && r.Closed == "" {
				open++
			}
			r.mu.Unlock()
		}
		return open == k
	})
	fmt.Printf("READY %d\n", open)
	bufio.NewReader(os.Stdin).ReadString('\n') // раннер снял память — отпускаем
	cancel()
	wg.Wait()
	return map[string]any{"scenario": "hold", "transport": transport, "requested": k, "open": open}
}
