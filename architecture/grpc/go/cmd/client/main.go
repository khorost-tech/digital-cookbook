// Клиент-проба стенда grpc на Go. Тот же набор подкоманд и тот же вывод JSON у
// java/client — так любая пара клиент-сервер ставит опыт одинаково.
//
//	check      один набор вызовов для проверки паритета
//	unary      один Unary: -mode, -token, -key/-fail-code/-fail-times, -retry, -size, -max-recv
//	chain      клиент → A → B: -next, -propagate, -deadline-ms, -steps, -step-ms
//	cancel     серверный стрим: прочитать -read сообщений и отменить
//	keepalive  долгий серверный стрим при пингах клиента раз в -ka-time-ms
//	idle       вызов, простой -idle-s, вызов (-ka-time-ms — пинги на простое)
//	balance    -n вызовов по одному каналу: -lb pick_first | round_robin
//	calls      -n вызовов с токеном и -n без (интерсепторы)
//	rest       вызов через gRPC-Gateway: -url, -path (unary | stream | chain), -mode, -grpc-timeout
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "tech.khorost/grpc/gen/demov1"
)

type opts struct {
	target, mode, token, key, failCode, retry, next, lb     string
	failTimes, size, maxRecv, steps, stepMs, count, read, n int
	intervalMs, kaTimeMs, deadlineMs                        int
	propagate, permit                                       bool
	idle, duration                                          time.Duration
	url, path, grpcTimeout                                  string
}

func dial(o opts, extra ...grpc.DialOption) *grpc.ClientConn {
	dopts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if o.retry != "" {
		dopts = append(dopts, grpc.WithDefaultServiceConfig(o.retry))
	} else if o.lb != "" {
		dopts = append(dopts, grpc.WithDefaultServiceConfig(fmt.Sprintf(`{"loadBalancingConfig":[{%q:{}}]}`, o.lb)))
	}
	if o.maxRecv > 0 {
		dopts = append(dopts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(o.maxRecv)))
	}
	if o.kaTimeMs > 0 {
		dopts = append(dopts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: time.Duration(o.kaTimeMs) * time.Millisecond, Timeout: 2 * time.Second, PermitWithoutStream: o.permit}))
	}
	cc, err := grpc.NewClient(o.target, append(dopts, extra...)...)
	if err != nil {
		log.Fatal(err)
	}
	return cc
}

func ctxFor(o opts) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	if o.token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+o.token)
	}
	if o.deadlineMs > 0 {
		return context.WithTimeout(ctx, time.Duration(o.deadlineMs)*time.Millisecond)
	}
	return context.WithCancel(ctx)
}

// result — код и детали ошибки словами стенда; у успеха code = OK.
func result(err error) map[string]any {
	s := status.Convert(err)
	out := map[string]any{"code": s.Code().String(), "message": s.Message(), "details_reason": ""}
	for _, d := range s.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			out["details_reason"] = ei.Reason
		}
	}
	return out
}

func healthy(target string) bool {
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
	return err == nil && r.Status == healthpb.HealthCheckResponse_SERVING
}

func unary(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	ctx, cancel := ctxFor(o)
	defer cancel()
	r, err := pb.NewDemoClient(cc).Unary(ctx, &pb.UnaryRequest{Mode: o.mode, Key: o.key, FailCode: o.failCode,
		FailTimes: int32(o.failTimes), Size: int32(o.size)})
	out := result(err)
	if err == nil {
		out["attempt"], out["instance"], out["payload_len"] = r.Attempt, r.Instance, len(r.Payload)
	}
	out["alive_after"] = healthy(o.target) // жив ли сервер после вызова — отдельным соединением
	return out
}

func chain(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	ctx, cancel := ctxFor(o)
	defer cancel()
	r, err := pb.NewDemoClient(cc).Chain(ctx, &pb.ChainRequest{Steps: int32(o.steps), StepMs: int32(o.stepMs),
		Next: o.next, Propagate: o.propagate})
	out := result(err)
	if err == nil {
		out["steps_done"] = r.StepsDone
	}
	return out
}

func cancelStream(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := pb.NewDemoClient(cc).ServerStream(ctx, &pb.StreamRequest{Count: int32(o.count), IntervalMs: int32(o.intervalMs)})
	if err != nil {
		cancel()
		return result(err)
	}
	got := 0
	for got < o.read {
		if _, err := s.Recv(); err != nil {
			break
		}
		got++
	}
	cancel()
	time.Sleep(time.Duration(o.count*o.intervalMs)*time.Millisecond + time.Second) // сервер дошёл до конца цикла
	return map[string]any{"received": got, "read": o.read, "count": o.count}
}

func keepaliveRun(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), o.duration)
	defer cancel()
	s, err := pb.NewDemoClient(cc).ServerStream(ctx, &pb.StreamRequest{Count: int32(o.count), IntervalMs: int32(o.intervalMs)})
	if err != nil {
		return result(err)
	}
	got := 0
	for {
		if _, err = s.Recv(); err != nil {
			break
		}
		got++
	}
	out := result(err)
	if err == io.EOF {
		out = map[string]any{"code": "OK", "message": "", "details_reason": ""}
	}
	out["received"] = got
	return out
}

func idle(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	c := pb.NewDemoClient(cc)
	// Локальный адрес TCP-соединения, по которому прошёл вызов: тот же адрес у
	// обоих вызовов — соединение пережило простой, разный — открыто новое.
	call := func() (map[string]any, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var p peer.Peer
		_, err := c.Unary(ctx, &pb.UnaryRequest{Mode: "ok"}, grpc.Peer(&p))
		local := ""
		if p.LocalAddr != nil {
			local = p.LocalAddr.String()
		}
		return result(err), local
	}
	first, firstLocal := call()
	time.Sleep(o.idle)
	// Состояние канала перед вторым вызовом — второстепенный признак: READY
	// бывает и после переподключения, поэтому исход решает адрес.
	state := cc.GetState().String()
	second, secondLocal := call()
	return map[string]any{"first": first["code"], "second": second["code"], "second_message": second["message"],
		"state_before_second": state, "first_local": firstLocal, "second_local": secondLocal,
		"idle_ms": o.idle.Milliseconds()}
}

func balance(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	c := pb.NewDemoClient(cc)
	per := map[string]int{}
	errs := 0
	for i := 0; i < o.n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		r, err := c.Unary(ctx, &pb.UnaryRequest{Mode: "ok"})
		cancel()
		if err != nil {
			errs++
			continue
		}
		per[r.Instance]++
	}
	return map[string]any{"calls": o.n, "errors": errs, "per_instance": per, "instances": len(per)}
}

// streamRetry — серверный стрим с политикой повторов из -retry: сколько
// сообщений пришло и чем кончилось.
func streamRetry(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := pb.NewDemoClient(cc).ServerStream(ctx, &pb.StreamRequest{Count: int32(o.count)})
	got := 0
	if err == nil {
		for {
			if _, err = s.Recv(); err != nil {
				break
			}
			got++
		}
	}
	out := result(err)
	if err == io.EOF {
		out = result(nil)
	}
	out["received"] = got
	return out
}

// rest — вызов через gRPC-Gateway: HTTP-статус и разобранное тело.
func rest(o opts) map[string]any {
	var req *http.Request
	switch o.path {
	case "stream":
		req, _ = http.NewRequest("GET", fmt.Sprintf("%s/v1/stream?count=%d", o.url, o.count), nil)
	case "chain":
		req, _ = http.NewRequest("POST", o.url+"/v1/chain",
			strings.NewReader(fmt.Sprintf(`{"steps":%d,"stepMs":%d}`, o.steps, o.stepMs)))
	default:
		req, _ = http.NewRequest("POST", o.url+"/v1/unary", strings.NewReader(fmt.Sprintf(`{"mode":%q,"key":"k"}`, o.mode)))
	}
	if o.grpcTimeout != "" {
		req.Header.Set("Grpc-Timeout", o.grpcTimeout)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return map[string]any{"http_status": 0, "error": err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[string]any{"http_status": resp.StatusCode, "content_type": resp.Header.Get("Content-Type")}
	// Ошибка шлюза — JSON google.rpc.Status: code (число), message, details.
	var st struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Details []json.RawMessage `json:"details"`
	}
	if o.path == "stream" {
		text := strings.TrimSpace(string(body))
		out["lines"] = strings.Count(text, "\n") + 1
		out["first_line"] = strings.SplitN(text, "\n", 2)[0]
	} else if resp.StatusCode != 200 && json.Unmarshal(body, &st) == nil {
		out["grpc_code"] = codes.Code(st.Code).String()
		out["details_in_body"] = len(st.Details)
		out["body_reason"] = ""
		for _, d := range st.Details {
			var ei struct {
				Reason string `json:"reason"`
			}
			if json.Unmarshal(d, &ei) == nil && ei.Reason != "" {
				out["body_reason"] = ei.Reason
			}
		}
	}
	return out
}

func calls(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	c := pb.NewDemoClient(cc)
	codesSeen := map[string]int{}
	for _, tok := range []string{"", "ok"} {
		for i := 0; i < o.n; i++ {
			ctx := context.Background()
			if tok != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := c.Unary(ctx, &pb.UnaryRequest{Mode: "ok"})
			cancel()
			codesSeen[status.Code(err).String()]++
		}
	}
	return map[string]any{"calls_without_token": o.n, "calls_with_token": o.n, "codes": codesSeen}
}

// check — набор паритета: одни и те же вызовы, наблюдения без имён инстансов.
func check(o opts) map[string]any {
	cc := dial(o)
	defer cc.Close()
	c := pb.NewDemoClient(cc)
	obs := map[string]any{}
	for _, m := range []string{"ok", "plain", "status", "details", "invalid", "unavailable"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.Unary(ctx, &pb.UnaryRequest{Mode: m, Key: "k"})
		cancel()
		r := result(err)
		obs["unary_"+m] = []any{r["code"], r["details_reason"]}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, _ := c.ServerStream(ctx, &pb.StreamRequest{Count: 3})
	ids := []int64{}
	for {
		it, err := s.Recv()
		if err != nil {
			break
		}
		ids = append(ids, it.Id)
	}
	obs["server_stream"] = ids
	cs, _ := c.ClientStream(ctx)
	for i := 1; i <= 3; i++ {
		cs.Send(&pb.Item{Id: int64(i)})
	}
	sum, err := cs.CloseAndRecv()
	if err == nil {
		obs["client_stream"] = sum.Count
	}
	bs, _ := c.Bidi(ctx)
	echo := []int64{}
	for i := 1; i <= 3; i++ {
		bs.Send(&pb.Item{Id: int64(i)})
		if it, err := bs.Recv(); err == nil {
			echo = append(echo, it.Id)
		}
	}
	bs.CloseSend()
	obs["bidi"] = echo
	r, err := c.Chain(ctx, &pb.ChainRequest{Steps: 2, StepMs: 10})
	if err == nil {
		obs["chain_steps"] = r.StepsDone
	}
	keys := make([]string, 0, len(obs))
	for k := range obs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	obs["_keys"] = len(keys)
	return obs
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("подкоманда: check|unary|chain|cancel|keepalive|idle|balance|calls")
	}
	var o opts
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	fs.StringVar(&o.target, "target", "server:9090", "адрес")
	fs.StringVar(&o.mode, "mode", "ok", "режим Unary")
	fs.StringVar(&o.token, "token", "", "токен в metadata authorization")
	fs.StringVar(&o.key, "key", "k", "ключ счётчика попыток")
	fs.StringVar(&o.failCode, "fail-code", "UNAVAILABLE", "код отказа")
	fs.IntVar(&o.failTimes, "fail-times", 0, "сколько первых попыток отказать")
	fs.StringVar(&o.retry, "retry", "", "service config JSON с retryPolicy")
	fs.IntVar(&o.size, "size", 0, "длина payload ответа")
	fs.IntVar(&o.maxRecv, "max-recv", 0, "предел размера принимаемого сообщения (0 — по умолчанию)")
	fs.StringVar(&o.next, "next", "", "следующее звено цепочки")
	fs.BoolVar(&o.propagate, "propagate", true, "звено передаёт контекст дальше")
	fs.IntVar(&o.deadlineMs, "deadline-ms", 0, "срок вызова")
	fs.IntVar(&o.steps, "steps", 10, "шагов работы исполнителя")
	fs.IntVar(&o.stepMs, "step-ms", 100, "длина шага")
	fs.IntVar(&o.count, "count", 20, "сообщений серверного стрима")
	fs.IntVar(&o.intervalMs, "interval-ms", 100, "пауза между сообщениями стрима")
	fs.IntVar(&o.read, "read", 3, "прочитать сообщений и отменить")
	fs.IntVar(&o.kaTimeMs, "ka-time-ms", 0, "пинг keepalive клиента (0 — выключен)")
	fs.BoolVar(&o.permit, "permit", false, "пинговать без активных вызовов")
	fs.DurationVar(&o.idle, "idle", 6*time.Second, "простой между вызовами")
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "длительность опыта keepalive")
	fs.StringVar(&o.lb, "lb", "", "политика балансировки клиента")
	fs.IntVar(&o.n, "n", 30, "вызовов")
	fs.StringVar(&o.url, "url", "http://gateway:8080", "адрес gRPC-Gateway")
	fs.StringVar(&o.path, "path", "unary", "unary | stream | chain")
	fs.StringVar(&o.grpcTimeout, "grpc-timeout", "", "заголовок Grpc-Timeout (например 300m)")
	fs.Parse(os.Args[2:])
	var out map[string]any
	switch os.Args[1] {
	case "check":
		out = check(o)
	case "unary":
		out = unary(o)
	case "chain":
		out = chain(o)
	case "cancel":
		out = cancelStream(o)
	case "keepalive":
		out = keepaliveRun(o)
	case "idle":
		out = idle(o)
	case "balance":
		out = balance(o)
	case "calls":
		out = calls(o)
	case "rest":
		out = rest(o)
	case "stream-retry":
		out = streamRetry(o)
	default:
		log.Fatalf("неизвестная подкоманда %q", os.Args[1])
	}
	out["client"] = "go"
	json.NewEncoder(os.Stdout).Encode(out)
}
