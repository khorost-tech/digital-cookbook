// gRPC-Gateway с зарегистрированными типами google.rpc.* — то же, что cmd/gateway,
// плюс одна строка: пустой импорт errdetails. Без неё шлюз не может разобрать
// Any с ErrorInfo в деталях ошибки и отвечает 500 вместо кода ошибки.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	_ "google.golang.org/genproto/googleapis/rpc/errdetails" // типы деталей ошибок для JSON
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "tech.khorost/grpc/gen/demov1"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	mux := runtime.NewServeMux()
	err := pb.RegisterDemoHandlerFromEndpoint(context.Background(), mux, env("GRPC_TARGET", "server:9090"),
		[]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("gateway-details :%s → %s", env("GATEWAY_PORT", "8080"), env("GRPC_TARGET", "server:9090"))
	log.Fatal(http.ListenAndServe(":"+env("GATEWAY_PORT", "8080"), mux))
}
