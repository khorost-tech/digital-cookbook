FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# Повторы: proxy.golang.org из Docker Desktop периодически рвёт TLS (bad record MAC).
RUN for i in 1 2 3 4 5; do go mod download && exit 0; sleep 3; done; exit 1
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/server .

FROM alpine:3.22
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
