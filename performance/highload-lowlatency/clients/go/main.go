// Command highload-go-client is the load-generating client for the
// highload/low-latency stand. It maintains a small pool of reusable
// cleartext HTTP/2 (h2c) connections, fires POST /check requests against
// HAProxy through that pool, and prints latency statistics plus the
// distribution of responses across backend instances (see
// performance/highload-lowlatency/README.md for the shared contract).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

const (
	defaultTarget      = "http://haproxy:8080/check"
	defaultConcurrency = 200
	defaultRequests    = 5000
	defaultConns       = 4
	defaultTimeoutMs   = 280
	defaultPayloadPath = "/payload/sample-request.json"
)

// fallbackPayload is used when no payload file is found at PAYLOAD (or the
// default path) so the client can still run standalone, e.g. for a quick
// smoke test outside the full stand topology.
const fallbackPayload = `{
  "request_id": "00000000-0000-0000-0000-000000000000",
  "issued_at": "2026-01-01T00:00:00Z",
  "items": [
    {"id": 1, "code": "ITEM-0001", "value": 1.0, "note": "placeholder-payload-used-because-no-PAYLOAD-file-was-found"}
  ]
}`

// config holds the client configuration, entirely sourced from env vars.
type config struct {
	target      string
	concurrency int
	requests    int
	conns       int
	timeout     time.Duration
	payload     []byte
}

func loadConfig() (config, error) {
	cfg := config{
		target:      getEnvDefault("TARGET", defaultTarget),
		concurrency: defaultConcurrency,
		requests:    defaultRequests,
		conns:       defaultConns,
		timeout:     defaultTimeoutMs * time.Millisecond,
	}

	var err error
	if cfg.concurrency, err = getEnvIntDefault("CONCURRENCY", defaultConcurrency); err != nil {
		return config{}, err
	}
	if cfg.requests, err = getEnvIntDefault("REQUESTS", defaultRequests); err != nil {
		return config{}, err
	}
	if cfg.conns, err = getEnvIntDefault("CONNS", defaultConns); err != nil {
		return config{}, err
	}
	timeoutMs, err := getEnvIntDefault("TIMEOUT_MS", defaultTimeoutMs)
	if err != nil {
		return config{}, err
	}
	cfg.timeout = time.Duration(timeoutMs) * time.Millisecond

	payloadPath := getEnvDefault("PAYLOAD", defaultPayloadPath)
	cfg.payload, err = loadPayload(payloadPath)
	if err != nil {
		return config{}, err
	}

	if cfg.concurrency <= 0 {
		return config{}, fmt.Errorf("CONCURRENCY must be positive, got %d", cfg.concurrency)
	}
	if cfg.requests <= 0 {
		return config{}, fmt.Errorf("REQUESTS must be positive, got %d", cfg.requests)
	}
	if cfg.conns <= 0 {
		return config{}, fmt.Errorf("CONNS must be positive, got %d", cfg.conns)
	}

	return cfg, nil
}

func loadPayload(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("warning: payload file %q not found, using built-in fallback payload", path)
			return []byte(fallbackPayload), nil
		}
		return nil, fmt.Errorf("reading payload %q: %w", path, err)
	}
	return data, nil
}

func getEnvDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func getEnvIntDefault(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("env %s: invalid integer %q: %w", name, v, err)
	}
	return n, nil
}

// pooledClient is one member of the H2 connection pool: a dedicated
// *http.Client backed by its own http2.Transport, plus an in-flight counter
// used for least-inflight load balancing across the pool.
type pooledClient struct {
	client   *http.Client
	inFlight atomic.Int64
}

// newH2CClient builds an *http.Client that speaks cleartext HTTP/2 (h2c).
// AllowHTTP lets the transport use h2 without TLS, and DialTLSContext is
// overridden to perform a plain net.Dial instead of a TLS handshake — this
// is what makes the connection cleartext while still negotiating HTTP/2
// framing end-to-end. Each pool member owns its own transport/connection so
// requests routed to it reuse the same underlying TCP connection.
func newH2CClient() *http.Client {
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: transport}
}

// requestOutcome captures the result of a single POST /check call.
type requestOutcome struct {
	latency time.Duration
	backend string
	kind    outcomeKind
}

type outcomeKind int

const (
	outcomeSuccess outcomeKind = iota
	outcomeTimeout
	outcomeError
)

// checkResponse mirrors the POST /check response contract documented in
// performance/highload-lowlatency/README.md.
type checkResponse struct {
	RequestID    string `json:"request_id"`
	Backend      string `json:"backend"`
	Runtime      string `json:"runtime"`
	CheckMs      int64  `json:"check_ms"`
	InFlightPeak int64  `json:"in_flight_peak"`
}

// results accumulates outcomes from all workers behind a mutex. Latencies
// are only recorded for successful requests, matching the report's p50/p95
// p99/min/max which describe completed round-trips.
type results struct {
	mu          sync.Mutex
	latencies   []time.Duration
	backendHits map[string]int
	successes   int
	timeouts    int
	errors      int
}

func newResults() *results {
	return &results{backendHits: make(map[string]int)}
}

func (r *results) record(o requestOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch o.kind {
	case outcomeSuccess:
		r.successes++
		r.latencies = append(r.latencies, o.latency)
		r.backendHits[o.backend]++
	case outcomeTimeout:
		r.timeouts++
	default:
		r.errors++
	}
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	log.Printf(
		"highload-go-client: target=%s concurrency=%d requests=%d conns=%d timeout=%s payload_bytes=%d",
		cfg.target, cfg.concurrency, cfg.requests, cfg.conns, cfg.timeout, len(cfg.payload),
	)

	pool := make([]*pooledClient, cfg.conns)
	for i := range pool {
		pool[i] = &pooledClient{client: newH2CClient()}
	}

	res := newResults()

	var nextRequest atomic.Int64 // shared counter: workers claim request indices [0, cfg.requests)
	var wg sync.WaitGroup
	wg.Add(cfg.concurrency)

	start := time.Now()
	for w := 0; w < cfg.concurrency; w++ {
		go func() {
			defer wg.Done()
			for {
				idx := nextRequest.Add(1) - 1
				if idx >= int64(cfg.requests) {
					return
				}
				pc := pickLeastInFlight(pool)
				res.record(doRequest(pc, cfg))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	printReport(cfg, res, elapsed)
}

// pickLeastInFlight selects the pool member with the fewest in-flight
// requests, so the CONCURRENCY workers are spread across the CONNS H2
// connections rather than piling requests onto a single one. On a tie it
// picks the lowest-indexed client (stable round-robin-ish behavior under
// steady load since inFlight is decremented as requests complete).
func pickLeastInFlight(pool []*pooledClient) *pooledClient {
	best := pool[0]
	bestLoad := best.inFlight.Load()
	for _, pc := range pool[1:] {
		load := pc.inFlight.Load()
		if load < bestLoad {
			best = pc
			bestLoad = load
		}
	}
	return best
}

// doRequest performs a single POST /check call using the given pool
// connection and turns the outcome into a requestOutcome.
func doRequest(pc *pooledClient, cfg config) requestOutcome {
	pc.inFlight.Add(1)
	defer pc.inFlight.Add(-1)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.target, bytes.NewReader(cfg.payload))
	if err != nil {
		return requestOutcome{kind: outcomeError}
	}
	req.Header.Set("Content-Type", "application/json")

	reqStart := time.Now()
	resp, err := pc.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return requestOutcome{kind: outcomeTimeout}
		}
		return requestOutcome{kind: outcomeError}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return requestOutcome{kind: outcomeError}
	}

	var body checkResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return requestOutcome{kind: outcomeError}
	}
	latency := time.Since(reqStart)

	return requestOutcome{
		latency: latency,
		backend: body.Backend,
		kind:    outcomeSuccess,
	}
}

// printReport prints latency percentiles/min/max, outcome counts, and the
// backend distribution table.
func printReport(cfg config, res *results, elapsed time.Duration) {
	res.mu.Lock()
	defer res.mu.Unlock()

	total := res.successes + res.timeouts + res.errors

	fmt.Println()
	fmt.Println("=== highload-go-client report ===")
	fmt.Printf("target:       %s\n", cfg.target)
	fmt.Printf("requests:     %d (concurrency=%d, conns=%d, timeout=%s)\n", cfg.requests, cfg.concurrency, cfg.conns, cfg.timeout)
	fmt.Printf("elapsed:      %s\n", elapsed)
	fmt.Println()

	fmt.Println("--- outcomes ---")
	fmt.Printf("total:        %d\n", total)
	fmt.Printf("successes:    %d\n", res.successes)
	fmt.Printf("timeouts:     %d\n", res.timeouts)
	fmt.Printf("errors:       %d\n", res.errors)
	fmt.Println()

	fmt.Println("--- latency (successful requests only) ---")
	if len(res.latencies) == 0 {
		fmt.Println("no successful requests, no latency stats available")
	} else {
		sorted := make([]time.Duration, len(res.latencies))
		copy(sorted, res.latencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

		fmt.Printf("min:          %s\n", sorted[0])
		fmt.Printf("p50:          %s\n", percentile(sorted, 50))
		fmt.Printf("p95:          %s\n", percentile(sorted, 95))
		fmt.Printf("p99:          %s\n", percentile(sorted, 99))
		fmt.Printf("max:          %s\n", sorted[len(sorted)-1])
	}
	fmt.Println()

	fmt.Println("--- backend distribution ---")
	if len(res.backendHits) == 0 {
		fmt.Println("no successful responses to attribute to a backend")
	} else {
		backends := make([]string, 0, len(res.backendHits))
		for b := range res.backendHits {
			backends = append(backends, b)
		}
		sort.Strings(backends)

		fmt.Printf("%-24s %10s %8s\n", "backend", "requests", "share")
		for _, b := range backends {
			count := res.backendHits[b]
			share := float64(count) / float64(res.successes) * 100
			fmt.Printf("%-24s %10d %7.2f%%\n", b, count, share)
		}
	}
}

// percentile returns the p-th percentile (0-100) of a pre-sorted duration
// slice using nearest-rank interpolation.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lo)
	return sorted[lo] + time.Duration(float64(sorted[hi]-sorted[lo])*frac)
}
