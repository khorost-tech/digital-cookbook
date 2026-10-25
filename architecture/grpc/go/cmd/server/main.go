// Сервер стенда grpc на Go. Тот же контракт и те же GRPC_* у java/server.
//
// Переменные окружения:
//
//	GRPC_INSTANCE              имя инстанса (в ответах и счётчиках)
//	GRPC_PORT, GRPC_STATS_PORT порт gRPC (9090) и HTTP-порт счётчиков (8081)
//	GRPC_STREAM_CHECK_CANCEL   1 — серверный стрим проверяет отмену перед отправкой
//	GRPC_RECOVER               1 — перехватчик паники (иначе паника роняет процесс)
//	GRPC_CHAIN_ORDER           none | auth-first | metrics-first — цепочка интерсепторов
//	GRPC_CHAIN                 цепочка списком снаружи внутрь: recover,auth,metrics,log
//	                           (перекрывает GRPC_CHAIN_ORDER и GRPC_RECOVER)
//	GRPC_STREAM_FAIL_AFTER     серверный стрим отвечает UNAVAILABLE после N сообщений
//	                           (-1 — выключено; 0 — до первого сообщения)
//	GRPC_STREAM_HEADERS_FIRST  1 — перед отказом стрима отправить заголовки ответа
//	GRPC_STREAM_SEND           только java: sync | async | async-handler | sync-handler —
//	                           где идёт цикл отправки стрима и есть ли setOnCancelHandler
//	GRPC_KA_MIN_TIME_MS        минимальный интервал пингов клиента (EnforcementPolicy.MinTime);
//	                           0 — по умолчанию библиотеки (5 минут)
//	GRPC_KA_PERMIT_NO_STREAM   1 — разрешать пинги без активных вызовов
//
// Счётчики: GET /stats, POST /reset на GRPC_STATS_PORT.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "tech.khorost/grpc/gen/demov1"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

var instance = env("GRPC_INSTANCE", "go-1")

// --- счётчики --------------------------------------------------------------------

var st struct {
	mu sync.Mutex
	// ось 1: звено-исполнитель
	chainCalls, stepsDone, stepsAfterCancel int64
	cancelSeen                              int64
	// ось 2: серверный стрим
	streamSent, streamSentAfterCancel, streamSendErrors int64
	streamCancelSeen, streamCalls                       int64
	// ось 4: интерсепторы
	metricsCalls, logCalls, handlerCalls int64
	metricsByCode                        map[string]int64
	// ось 5: попытки по ключу
	attempts map[string]int64
	// оси 6, 7: соединения
	conns, calls int64
}

func resetStats() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.chainCalls, st.stepsDone, st.stepsAfterCancel, st.cancelSeen = 0, 0, 0, 0
	st.streamSent, st.streamSentAfterCancel, st.streamSendErrors, st.streamCancelSeen = 0, 0, 0, 0
	st.streamCalls = 0
	st.metricsCalls, st.logCalls, st.handlerCalls = 0, 0, 0
	st.metricsByCode = map[string]int64{}
	st.attempts = map[string]int64{}
	st.conns, st.calls = 0, 0
}

func statsJSON() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return map[string]any{"instance": instance, "server": "go",
		"chain_calls": st.chainCalls, "steps_done": st.stepsDone, "steps_after_cancel": st.stepsAfterCancel,
		"cancel_seen": st.cancelSeen,
		"stream_sent": st.streamSent, "stream_sent_after_cancel": st.streamSentAfterCancel,
		"stream_send_errors": st.streamSendErrors, "stream_cancel_seen": st.streamCancelSeen,
		"stream_calls":  st.streamCalls,
		"metrics_calls": st.metricsCalls, "log_calls": st.logCalls, "handler_calls": st.handlerCalls,
		"metrics_by_code": st.metricsByCode, "attempts": st.attempts, "conns": st.conns, "calls": st.calls}
}

func add(p *int64, n int64) {
	st.mu.Lock()
	*p += n
	st.mu.Unlock()
}

// countingListener — сколько TCP-соединений принял сервер (оси 6, 7).
type countingListener struct{ net.Listener }

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		add(&st.conns, 1)
	}
	return c, err
}

// --- сервис ----------------------------------------------------------------------

type demo struct {
	pb.UnimplementedDemoServer
	checkCancel  bool
	failAfter    int
	headersFirst bool
	mu           sync.Mutex
	clients      map[string]pb.DemoClient
}

// attemptOf — номер попытки: gRPC пишет в метаданные повторной попытки
// grpc-previous-rpc-attempts; у первой попытки заголовка нет.
func attemptOf(ctx context.Context) int32 {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("grpc-previous-rpc-attempts"); len(v) > 0 {
		n, _ := strconv.Atoi(v[0])
		return int32(n) + 1
	}
	return 1
}

func (d *demo) Unary(ctx context.Context, r *pb.UnaryRequest) (*pb.UnaryReply, error) {
	add(&st.handlerCalls, 1)
	add(&st.calls, 1)
	attempt := attemptOf(ctx)
	switch r.Mode {
	case "plain":
		return nil, errors.New("запись не найдена") // обычная ошибка языка
	case "status":
		return nil, status.Error(codes.NotFound, "запись не найдена")
	case "details":
		s, _ := status.New(codes.NotFound, "запись не найдена").WithDetails(
			&errdetails.ErrorInfo{Reason: "RECORD_NOT_FOUND", Domain: "demo.khorost.tech",
				Metadata: map[string]string{"key": r.Key}})
		return nil, s.Err()
	case "invalid":
		return nil, status.Error(codes.InvalidArgument, "ключ пуст")
	case "unavailable":
		return nil, status.Error(codes.Unavailable, "зависимость недоступна")
	case "panic":
		var m map[string]int
		m["x"] = 1 // nil map — паника в обработчике
	case "fail":
		st.mu.Lock()
		st.attempts[r.Key]++
		n := st.attempts[r.Key]
		st.mu.Unlock()
		if n <= int64(r.FailTimes) {
			code := codes.Unavailable
			for c := codes.OK; c <= codes.Unauthenticated; c++ {
				if c.String() == toCamel(r.FailCode) {
					code = c
				}
			}
			return nil, status.Errorf(code, "отказ %d из %d", n, r.FailTimes)
		}
	}
	return &pb.UnaryReply{Instance: instance, Attempt: attempt, Payload: make([]byte, r.Size)}, nil
}

// toCamel: UNAVAILABLE → Unavailable, DEADLINE_EXCEEDED → DeadlineExceeded.
func toCamel(s string) string {
	parts := strings.Split(strings.ToLower(s), "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func (d *demo) ServerStream(r *pb.StreamRequest, s pb.Demo_ServerStreamServer) error {
	add(&st.streamCalls, 1)
	ctx := s.Context()
	cancelled := false
	for i := int32(1); i <= r.Count; i++ {
		if d.failAfter >= 0 && int(i)-1 == d.failAfter { // граница повторов (ось 5)
			if d.headersFirst { // заголовки ответа до отказа — вызов уже не trailers-only
				_ = s.SendHeader(metadata.MD{})
			}
			return status.Error(codes.Unavailable, "отказ стрима")
		}
		if !cancelled && ctx.Err() != nil {
			cancelled = true
			add(&st.streamCancelSeen, 1)
		}
		if cancelled && d.checkCancel {
			return status.FromContextError(ctx.Err()).Err()
		}
		err := s.Send(&pb.Item{Id: int64(i), Text: "item"})
		switch {
		case err != nil:
			add(&st.streamSendErrors, 1)
		case cancelled:
			add(&st.streamSentAfterCancel, 1)
		default:
			add(&st.streamSent, 1)
		}
		if i < r.Count { // пауза — между сообщениями, после последнего стрим закрывается сразу
			time.Sleep(time.Duration(r.IntervalMs) * time.Millisecond)
		}
	}
	return nil
}

func (d *demo) ClientStream(s pb.Demo_ClientStreamServer) error {
	n := int32(0)
	for {
		_, err := s.Recv()
		if err == io.EOF {
			return s.SendAndClose(&pb.Summary{Count: n})
		}
		if err != nil {
			return err
		}
		n++
	}
}

func (d *demo) Bidi(s pb.Demo_BidiServer) error {
	for {
		it, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.Send(it); err != nil {
			return err
		}
	}
}

func (d *demo) client(addr string) (pb.DemoClient, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[addr]; ok {
		return c, nil
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	c := pb.NewDemoClient(cc)
	d.clients[addr] = c
	return c, nil
}

func (d *demo) Chain(ctx context.Context, r *pb.ChainRequest) (*pb.ChainReply, error) {
	if r.Next != "" { // звено-посредник
		next, err := d.client(r.Next)
		if err != nil {
			return nil, err
		}
		callCtx := ctx
		if !r.Propagate {
			// Ошибка, которую ставит опыт: новый контекст без срока и без отмены —
			// нижнее звено не узнает, что клиент уже ушёл.
			callCtx = context.Background()
		}
		return next.Chain(callCtx, &pb.ChainRequest{Steps: r.Steps, StepMs: r.StepMs})
	}
	add(&st.chainCalls, 1) // исполнитель: работа шагами, отмена проверяется перед шагом
	done := int32(0)
	seen := false
	for i := int32(0); i < r.Steps; i++ {
		if ctx.Err() != nil {
			if !seen {
				seen = true
				add(&st.cancelSeen, 1)
			}
			break
		}
		time.Sleep(time.Duration(r.StepMs) * time.Millisecond)
		done++
		add(&st.stepsDone, 1)
	}
	if !seen && ctx.Err() != nil { // отмена пришла во время последнего шага
		add(&st.cancelSeen, 1)
	}
	return &pb.ChainReply{Instance: instance, StepsDone: done}, nil
}

// --- интерсепторы ----------------------------------------------------------------

// demoOnly — интерсепторы опытов видят только методы Demo: health-вызовы, которыми
// проба проверяет, жив ли сервер, иначе попали бы в метрики (как в Java, где
// health зарегистрирован без интерсепторов).
func demoOnly(info *grpc.UnaryServerInfo) bool {
	return strings.HasPrefix(info.FullMethod, "/demo.v1.Demo/")
}

func authInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if !demoOnly(info) {
		return h(ctx, req)
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) == 0 || v[0] != "Bearer ok" {
		return nil, status.Error(codes.Unauthenticated, "нет токена")
	}
	return h(ctx, req)
}

func metricsInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if !demoOnly(info) {
		return h(ctx, req)
	}
	resp, err := h(ctx, req)
	st.mu.Lock()
	st.metricsCalls++
	st.metricsByCode[status.Code(err).String()]++
	st.mu.Unlock()
	return resp, err
}

func logInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if !demoOnly(info) {
		return h(ctx, req)
	}
	add(&st.logCalls, 1)
	return h(ctx, req)
}

func recoverInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = status.Errorf(codes.Internal, "паника в обработчике: %v", p)
		}
	}()
	return h(ctx, req)
}

func main() {
	resetStats()
	var chain []grpc.UnaryServerInterceptor
	if list := env("GRPC_CHAIN", ""); list != "" {
		// Снаружи внутрь: первый в списке вызывается первым и видит итог последним.
		byName := map[string]grpc.UnaryServerInterceptor{"recover": recoverInterceptor, "auth": authInterceptor,
			"metrics": metricsInterceptor, "log": logInterceptor}
		for _, n := range strings.Split(list, ",") {
			chain = append(chain, byName[n])
		}
	} else {
		if env("GRPC_RECOVER", "0") == "1" {
			chain = append(chain, recoverInterceptor)
		}
		switch env("GRPC_CHAIN_ORDER", "none") {
		case "auth-first":
			chain = append(chain, authInterceptor, metricsInterceptor, logInterceptor)
		case "metrics-first":
			chain = append(chain, metricsInterceptor, logInterceptor, authInterceptor)
		}
	}
	// Стенд подключает только unary-интерсепторы: стримы ими не защищены и не
	// посчитаны — для них нужна отдельная цепочка grpc.ChainStreamInterceptor.
	opts := []grpc.ServerOption{grpc.ChainUnaryInterceptor(chain...)}
	if ms, _ := strconv.Atoi(env("GRPC_KA_MIN_TIME_MS", "0")); ms > 0 || env("GRPC_KA_PERMIT_NO_STREAM", "0") == "1" {
		pol := keepalive.EnforcementPolicy{PermitWithoutStream: env("GRPC_KA_PERMIT_NO_STREAM", "0") == "1"}
		if ms > 0 {
			pol.MinTime = time.Duration(ms) * time.Millisecond
		} else {
			pol.MinTime = 5 * time.Minute // значение библиотеки по умолчанию
		}
		opts = append(opts, grpc.KeepaliveEnforcementPolicy(pol))
	}
	srv := grpc.NewServer(opts...)
	failAfter, _ := strconv.Atoi(env("GRPC_STREAM_FAIL_AFTER", "-1"))
	pb.RegisterDemoServer(srv, &demo{checkCancel: env("GRPC_STREAM_CHECK_CANCEL", "0") == "1", failAfter: failAfter,
		headersFirst: env("GRPC_STREAM_HEADERS_FIRST", "0") == "1",
		clients:      map[string]pb.DemoClient{}})
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(statsJSON())
		})
		mux.HandleFunc("POST /reset", func(w http.ResponseWriter, _ *http.Request) {
			resetStats()
			w.WriteHeader(http.StatusNoContent)
		})
		log.Fatal(http.ListenAndServe(":"+env("GRPC_STATS_PORT", "8081"), mux))
	}()
	ln, err := net.Listen("tcp", ":"+env("GRPC_PORT", "9090"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s: gRPC %s, интерсепторы %s, recover %s", instance, ln.Addr(),
		env("GRPC_CHAIN_ORDER", "none"), env("GRPC_RECOVER", "0"))
	log.Fatal(srv.Serve(countingListener{ln}))
}
