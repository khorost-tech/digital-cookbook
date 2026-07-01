// Command highload-grpc-go-client is the gRPC load-generating client for the
// highload/low-latency stand. It is the gRPC counterpart of
// clients/go/main.go: same pool/least-inflight/latency-stats approach, but
// calling highload.CheckService/Check over a pool of *grpc.ClientConn
// instead of POSTing JSON over an H2 *http.Client pool (see
// performance/highload-lowlatency/README.md for the shared contract).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	highloadpb "khorost.tech/highload-grpc-gen/highload"
)

const (
	// defaultTarget points at the stand's gRPC front-end (HAProxy fe_grpc on
	// :8090 → be_grpc pool). It can also be pointed directly at a backend,
	// e.g. TARGET=grpc-backend-1:9100 or TARGET=localhost:19100 for a
	// standalone smoke test.
	defaultTarget      = "haproxy:8090"
	defaultConcurrency = 200
	defaultRequests    = 5000
	defaultConns       = 4
	defaultTimeoutMs   = 280
	defaultPayloadPath = "/payload/sample-request.json"
)

// fallbackPayload mirrors clients/go's fallback: used when no payload file
// is found at PAYLOAD (or the default path), so the client can still run
// standalone, e.g. for a quick smoke test outside the full stand topology.
const fallbackPayload = `{
  "request_id": "00000000-0000-0000-0000-000000000000",
  "issued_at": "2026-01-01T00:00:00Z",
  "items": [
    {"id": 1, "code": "ITEM-0001", "value": 1.0, "note": "placeholder-payload-used-because-no-PAYLOAD-file-was-found"}
  ]
}`

// jsonItem/jsonCheckRequest mirror the shared JSON request schema (see
// README.md and topology/payload/sample-request.json) purely as an
// unmarshalling target. The client reads the very same ~8KB payload file
// used by clients/go and clients/java and re-packs it field-for-field into
// highloadpb.CheckRequest, rather than maintaining a second, independently
// sized payload generator for the gRPC path — this keeps the ~8KB body size
// and its shape identical across REST and gRPC clients, which matters for
// comparing the two transports on the same stand.
type jsonItem struct {
	ID    int64   `json:"id"`
	Code  string  `json:"code"`
	Value float64 `json:"value"`
	Note  string  `json:"note"`
}

type jsonCheckRequest struct {
	RequestID string     `json:"request_id"`
	IssuedAt  string     `json:"issued_at"`
	Items     []jsonItem `json:"items"`
}

// config holds the client configuration, entirely sourced from env vars.
type config struct {
	target      string
	concurrency int
	requests    int
	conns       int
	timeout     time.Duration
	request     *highloadpb.CheckRequest
	payloadSize int
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
	raw, err := loadPayloadBytes(payloadPath)
	if err != nil {
		return config{}, err
	}
	cfg.payloadSize = len(raw)

	cfg.request, err = decodeCheckRequest(raw)
	if err != nil {
		return config{}, fmt.Errorf("decoding payload %q as CheckRequest JSON: %w", payloadPath, err)
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

func loadPayloadBytes(path string) ([]byte, error) {
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

// decodeCheckRequest unmarshals the shared JSON payload and re-packs it into
// a *highloadpb.CheckRequest built once at startup and reused (read-only)
// across all calls, avoiding repeated JSON decoding/proto construction on
// the hot path.
func decodeCheckRequest(raw []byte) (*highloadpb.CheckRequest, error) {
	var parsed jsonCheckRequest
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}

	items := make([]*highloadpb.Item, len(parsed.Items))
	for i, it := range parsed.Items {
		items[i] = &highloadpb.Item{
			Id:    it.ID,
			Code:  it.Code,
			Value: it.Value,
			Note:  it.Note,
		}
	}

	return &highloadpb.CheckRequest{
		RequestId: parsed.RequestID,
		IssuedAt:  parsed.IssuedAt,
		Items:     items,
	}, nil
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

// pooledConn is one member of the gRPC channel pool: a *grpc.ClientConn plus
// an in-flight counter used for least-inflight load balancing across the
// pool, and a ready-made CheckServiceClient stub bound to it.
type pooledConn struct {
	conn     *grpc.ClientConn
	client   highloadpb.CheckServiceClient
	inFlight atomic.Int64
}

// newChannel dials a cleartext (h2c prior-knowledge) gRPC channel to target.
// grpc.NewClient (not the deprecated grpc.Dial) with insecure transport
// credentials is what makes the connection cleartext HTTP/2 — the same
// prior-knowledge transport grpc-go's server side always speaks (see
// services/grpc-backend/main.go) — which is exactly what HAProxy's `proto
// h2` front-end (fe_grpc on :8090) and the plain grpc-backend both expect.
// grpc.NewClient does not dial eagerly; the first RPC establishes the
// connection lazily, which is fine here since the pool is built once at
// startup, well before the load loop begins.
func newChannel(target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// requestOutcome captures the result of a single Check RPC.
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

// results accumulates outcomes from all workers behind a mutex. Latencies
// are only recorded for successful requests, matching the report's
// p50/p95/p99/min/max which describe completed round-trips.
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
		"highload-grpc-go-client: target=%s concurrency=%d requests=%d conns=%d timeout=%s payload_bytes=%d items=%d",
		cfg.target, cfg.concurrency, cfg.requests, cfg.conns, cfg.timeout, cfg.payloadSize, len(cfg.request.GetItems()),
	)

	pool := make([]*pooledConn, cfg.conns)
	for i := range pool {
		conn, err := newChannel(cfg.target)
		if err != nil {
			log.Fatalf("dialing channel %d/%d to %s: %v", i+1, cfg.conns, cfg.target, err)
		}
		defer conn.Close()
		pool[i] = &pooledConn{conn: conn, client: highloadpb.NewCheckServiceClient(conn)}
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
// requests, so the CONCURRENCY workers are spread across the CONNS gRPC
// channels rather than piling requests onto a single one. On a tie it picks
// the lowest-indexed channel (stable round-robin-ish behavior under steady
// load since inFlight is decremented as requests complete). Same approach as
// clients/go/main.go's pickLeastInFlight, just over *grpc.ClientConn instead
// of *http.Client.
func pickLeastInFlight(pool []*pooledConn) *pooledConn {
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

// doRequest performs a single Check RPC using the given pool channel and
// turns the outcome into a requestOutcome. WaitForReady(false) (the gRPC
// default) is used explicitly so a not-yet-ready channel fails/deadlines
// fast per-call instead of queuing behind connection establishment — the
// same "each call has its own budget" behavior as the REST client's
// per-request context.
func doRequest(pc *pooledConn, cfg config) requestOutcome {
	pc.inFlight.Add(1)
	defer pc.inFlight.Add(-1)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	reqStart := time.Now()
	resp, err := pc.client.Check(ctx, cfg.request, grpc.WaitForReady(false))
	if err != nil {
		if status.Code(err) == codes.DeadlineExceeded {
			return requestOutcome{kind: outcomeTimeout}
		}
		return requestOutcome{kind: outcomeError}
	}
	latency := time.Since(reqStart)

	return requestOutcome{
		latency: latency,
		backend: resp.GetBackend(),
		kind:    outcomeSuccess,
	}
}

// printReport prints latency percentiles/min/max, outcome counts, and the
// backend distribution table. Identical shape to clients/go's printReport so
// REST and gRPC runs are directly comparable.
func printReport(cfg config, res *results, elapsed time.Duration) {
	res.mu.Lock()
	defer res.mu.Unlock()

	total := res.successes + res.timeouts + res.errors

	fmt.Println()
	fmt.Println("=== highload-grpc-go-client report ===")
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
