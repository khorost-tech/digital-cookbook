# Собирать из корня модуля security/auth:
#   docker build -f client-refresh/Dockerfile .
FROM golang:1.26-alpine AS builder
ENV GOPROXY=https://go.khorost.tech,direct
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/client-refresh ./client-refresh

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/client-refresh /usr/local/bin/client-refresh
COPY client-refresh/web /web
WORKDIR /
ENV STATIC_DIR=/web
EXPOSE 8085
ENTRYPOINT ["/usr/local/bin/client-refresh"]
