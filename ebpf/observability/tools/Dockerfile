# Генератор запросов, запросы к Pyroscope, bpftool — счётчики eBPF-программ (ось цены).
FROM golang:1.27@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
RUN for i in 1 2 3; do apt-get update -qq && apt-get install -y -qq --no-install-recommends python3 bpftool procps && break; sleep 5; done \
 && rm -rf /var/lib/apt/lists/*
ENV GOFLAGS=-buildvcs=false GOCACHE=/cache/go-build GOMODCACHE=/cache/go-mod
WORKDIR /opt/stand
CMD ["sleep", "infinity"]
