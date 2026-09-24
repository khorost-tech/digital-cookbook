# Собирать из корня модуля security/auth:
#   docker build -f login-methods/Dockerfile .
FROM golang:1.26-alpine AS builder
ENV GOPROXY=https://go.khorost.tech,direct
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/login-methods ./login-methods

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/login-methods /usr/local/bin/login-methods
EXPOSE 8084
ENTRYPOINT ["/usr/local/bin/login-methods"]
