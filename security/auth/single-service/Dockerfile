# Собирать из корня модуля security/auth:
#   docker build -f single-service/Dockerfile .
FROM golang:1.26-alpine AS builder
ENV GOPROXY=https://go.khorost.tech,direct
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/single-service ./single-service

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/single-service /usr/local/bin/single-service
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/single-service"]
