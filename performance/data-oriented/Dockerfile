# Образ стенда для Linux-хоста с аппаратными счётчиками (devproxy, amd64).
# golang:1.27.2-trixie, собран 08.10.2026; clang и linux-perf — из Debian 13.
FROM golang:1.27.2-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e
RUN apt-get update && apt-get install -y --no-install-recommends clang linux-perf && rm -rf /var/lib/apt/lists/*
WORKDIR /stand
COPY go/go.mod go/go.sum go/
RUN cd go && go mod download
COPY c c
COPY go go
RUN clang -O2 -pthread -o dod-c c/dod.c && cd go && CGO_ENABLED=0 go build -o ../dod-go .
