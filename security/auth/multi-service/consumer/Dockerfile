# Собирать из корня модуля security/auth:
#   docker build -f multi-service/consumer/Dockerfile .
FROM golang:1.26-alpine AS builder
ENV GOPROXY=https://go.khorost.tech,direct
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/consumer ./multi-service/consumer

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/consumer /usr/local/bin/consumer
EXPOSE 8083
ENTRYPOINT ["/usr/local/bin/consumer"]
