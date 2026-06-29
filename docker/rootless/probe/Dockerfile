# Multi-stage: компактный статический бинарник.
# Контейнер НАМЕРЕННО работает от root внутри — чтобы показать,
# как rootless Docker маппит этот root на непривилегированного
# пользователя хоста (см. README, раздел про владельца файла в volume).
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /probe .

FROM alpine:3.20
COPY --from=builder /probe /usr/local/bin/probe
EXPOSE 8080
ENTRYPOINT ["probe"]
