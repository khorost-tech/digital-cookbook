# Сборка и запуск eBPF-программ: Go 1.27 + cilium/ebpf (bpf2go), clang/llvm, libbpf, bpftool, bpftrace.
FROM golang:1.27@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
RUN for i in 1 2 3; do apt-get update -qq && apt-get install -y -qq --no-install-recommends \
      clang llvm libbpf-dev bpftool bpftrace linux-libc-dev python3 && break; sleep 5; done \
 && rm -rf /var/lib/apt/lists/* \
 && ln -s /usr/include/$(uname -m)-linux-gnu/asm /usr/include/asm \
 && clang --version | head -1 && bpftool version | head -1 && bpftrace --version
ENV GOFLAGS=-buildvcs=false GOCACHE=/cache/go-build GOMODCACHE=/cache/go-mod
WORKDIR /opt/stand
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
CMD ["sleep", "infinity"]
