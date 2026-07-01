// Command highload-grpc-backend is the gRPC reference backend for the
// highload/low-latency stand. It speaks cleartext HTTP/2 (h2c, gRPC's
// standard prior-knowledge transport — grpc-go never negotiates TLS unless
// credentials are explicitly configured, so a plain grpc.NewServer() over a
// TCP listener already serves h2c, no golang.org/x/net/http2/h2c wrapper
// needed) and implements the gRPC equivalent of the shared contract
// described in performance/highload-lowlatency/README.md: rpc
// highload.CheckService/Check plus the standard gRPC health service.
package main

import (
	"context"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	highloadpb "khorost.tech/highload-grpc-gen/highload"
)

const (
	defaultListenAddr = ":9100"
	defaultBackend    = "grpc-?"

	simMinDelay = 100 * time.Millisecond
	simMaxJit   = 101 // rand.Intn(101) -> 0..100 inclusive, milliseconds
)

// checkServer implements highloadpb.CheckServiceServer and holds the shared
// in-flight counters for the backend instance, mirroring the go-backend
// (services/go-backend/main.go) server struct.
type checkServer struct {
	highloadpb.UnimplementedCheckServiceServer

	backendName string

	inFlight     atomic.Int64
	inFlightPeak atomic.Int64
}

// Check implements the unary rpc Check: simulates a synchronous payload
// check with a random 100-200ms delay and reports in-flight concurrency
// stats, matching POST /check in services/go-backend and
// services/java-backend.
func (s *checkServer) Check(ctx context.Context, req *highloadpb.CheckRequest) (*highloadpb.CheckResponse, error) {
	current := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	s.bumpPeak(current)

	delay := simMinDelay + time.Duration(rand.Intn(simMaxJit))*time.Millisecond
	start := time.Now()
	time.Sleep(delay)
	checkMs := time.Since(start).Milliseconds()

	return &highloadpb.CheckResponse{
		RequestId:    req.GetRequestId(),
		Backend:      s.backendName,
		Runtime:      "go-grpc",
		CheckMs:      checkMs,
		InFlightPeak: s.inFlightPeak.Load(),
	}, nil
}

// bumpPeak atomically updates inFlightPeak to be at least current, using a
// compare-and-swap loop so concurrent updates never lose a higher value.
func (s *checkServer) bumpPeak(current int64) {
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

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", listenAddr, err)
	}

	// grpc.NewServer with no transport credentials serves plain-text HTTP/2
	// (h2c) by default — grpc-go always speaks HTTP/2 framing over the raw
	// TCP connection it accepts, and only wraps it in TLS when
	// grpc.Creds(...) is supplied. There is no cleartext-upgrade handshake
	// step to opt into (unlike net/http, which needs golang.org/x/net/
	// http2/h2c to accept prior-knowledge h2c); the gRPC wire protocol
	// requires HTTP/2 prior-knowledge on both ends, so this is h2c
	// prior-knowledge by construction.
	grpcServer := grpc.NewServer()

	checkSrv := &checkServer{backendName: backendName}
	highloadpb.RegisterCheckServiceServer(grpcServer, checkSrv)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("highload.CheckService", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthSrv)

	go func() {
		log.Printf("highload-grpc-backend %q listening on %s (h2c)", backendName, listenAddr)
		if serveErr := grpcServer.Serve(lis); serveErr != nil {
			log.Fatalf("server stopped: %v", serveErr)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM: stop accepting new RPCs and let
	// in-flight ones finish before the process exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("highload-grpc-backend %q shutting down", backendName)
	grpcServer.GracefulStop()
}
