# Multi-stage сборка. Порядок слоёв выбран НАМЕРЕННО правильным: манифесты модуля
# копируются и зависимости скачиваются ДО копирования исходников. Пока go.mod/go.sum
# не менялись, слой `go mod download` берётся из кэша при каждой пересборке, даже
# если правился код. Скрипт layer-cache-demo.sh показывает цену обратного порядка.
FROM golang:1.26-alpine AS builder
WORKDIR /src

# Слой зависимостей: инвалидируется только при изменении go.mod/go.sum.
COPY go.mod go.sum ./
RUN go mod download

# Слой исходников: меняется при каждой правке кода, но слой выше уже готов.
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app .

FROM alpine:3.21
COPY --from=builder /out/app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
