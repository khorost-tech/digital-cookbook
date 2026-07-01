// Command highload-go-backend is the Go reference backend for the
// highload/low-latency stand. It speaks cleartext HTTP/2 (h2c) and
// implements the shared contract described in
// performance/highload-lowlatency/README.md: POST /check and GET /healthz.
package main

import (
	"encoding/json"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// checkRequest is the subset of the POST /check request body we care about:
// we only need to echo request_id back, the rest of the payload (items) is
// read and discarded to correctly drain the ~8KB body.
type checkRequest struct {
	RequestID string `json:"request_id"`
}

// checkResponse is the JSON response contract for POST /check.
type checkResponse struct {
	RequestID    string `json:"request_id"`
	Backend      string `json:"backend"`
	Runtime      string `json:"runtime"`
	CheckMs      int64  `json:"check_ms"`
	InFlightPeak int64  `json:"in_flight_peak"`
}

const (
	defaultListenAddr = ":9000"
	defaultBackend    = "go-?"

	simMinDelay = 100 * time.Millisecond
	simMaxJit   = 101 // rand.Intn(101) -> 0..100 inclusive, milliseconds
)

// server holds the shared in-flight counters for the backend instance.
type server struct {
	backendName string

	inFlight     atomic.Int64
	inFlightPeak atomic.Int64
}

func main() {
	backendName := os.Getenv("BACKEND_NAME")
	if backendName == "" {
		backendName = defaultBackend
		log.Printf("warning: BACKEND_NAME is not set, using default %q", backendName)
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	srv := &server{backendName: backendName}

	mux := http.NewServeMux()
	mux.HandleFunc("/check", srv.handleCheck)
	mux.HandleFunc("/healthz", srv.handleHealthz)

	h2s := &http2.Server{}
	httpServer := &http.Server{
		Addr:    listenAddr,
		Handler: h2c.NewHandler(mux, h2s),
	}

	log.Printf("highload-go-backend %q listening on %s (h2c)", backendName, listenAddr)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server stopped: %v", err)
	}
}

// handleCheck implements POST /check: simulates a synchronous payload check
// with a random 100-200ms delay and reports in-flight concurrency stats.
func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	current := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	s.bumpPeak(current)

	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Still drain and close the body before responding with an error.
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// Decode may not consume the whole body (extra bytes/padding); drain the
	// remainder so the connection can be reused cleanly.
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()

	delay := simMinDelay + time.Duration(rand.Intn(simMaxJit))*time.Millisecond
	start := time.Now()
	time.Sleep(delay)
	checkMs := time.Since(start).Milliseconds()

	resp := checkResponse{
		RequestID:    req.RequestID,
		Backend:      s.backendName,
		Runtime:      "go",
		CheckMs:      checkMs,
		InFlightPeak: s.inFlightPeak.Load(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("error writing response: %v", err)
	}
}

// handleHealthz implements GET /healthz.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// bumpPeak atomically updates inFlightPeak to be at least current, using a
// compare-and-swap loop so concurrent updates never lose a higher value.
func (s *server) bumpPeak(current int64) {
	for {
		peak := s.inFlightPeak.Load()
		if current <= peak {
			return
		}
		if s.inFlightPeak.CompareAndSwap(peak, current) {
			return
		}
	}
}
