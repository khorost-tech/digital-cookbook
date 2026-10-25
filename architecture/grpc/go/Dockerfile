# Один образ на сервер, клиент-пробу и шлюз Go: бинарник — первым аргументом
# (`docker run grpc-go server`). Код из proto/ уже сгенерирован в gen/ (proto/gen.sh).
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# Повторы: proxy.golang.org из Docker Desktop периодически рвёт TLS.
RUN for i in 1 2 3 4 5; do go mod download && exit 0; sleep 3; done; exit 1
COPY gen ./gen
COPY cmd ./cmd
RUN for c in server client gateway gateway-details; do CGO_ENABLED=0 go build -trimpath -o /out/$c ./cmd/$c; done

FROM alpine:3.22
COPY --from=build /out/ /usr/local/bin/
EXPOSE 9090 8081 8080
