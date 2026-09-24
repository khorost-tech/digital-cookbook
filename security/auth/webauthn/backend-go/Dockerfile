# Собирать из корня модуля security/auth:
#   docker build -f webauthn/backend-go/Dockerfile .
FROM golang:1.26-alpine AS builder
ENV GOPROXY=https://go.khorost.tech,direct
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/webauthn-go ./webauthn/backend-go

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/webauthn-go /usr/local/bin/webauthn-go
# Frontend (Task 6: web/index.html+app.js) — раздаётся тем же процессом с
# того же origin, что и /register|login/* (см. STATIC_DIR в main.go).
COPY --from=builder /src/webauthn/web /web
EXPOSE 8087
ENTRYPOINT ["/usr/local/bin/webauthn-go"]
