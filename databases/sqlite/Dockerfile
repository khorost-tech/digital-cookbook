# cgo обязателен: mattn/go-sqlite3 и go-duckdb собираются с нативными библиотеками.
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=1
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./
RUN go build -o /out/bench ./cmd/bench
RUN go build -o /out/olap ./cmd/olap

# Runtime с glibc: со scratch cgo-бинарник не запустится.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates postgresql-client sqlite3 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/bench /usr/local/bin/bench
COPY --from=build /out/olap /usr/local/bin/olap
COPY sql/ /sql/
WORKDIR /
