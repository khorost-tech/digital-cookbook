# Инструменты стенда: Go 1.27 + clang/libbpf/bpftool (XDP/tc на cilium/ebpf), kind, kubectl, helm,
# cilium CLI и docker CLI — kind создаёт узлы кластера через сокет Docker хоста.
FROM golang:1.27@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
ARG KIND=v0.33.0 KUBECTL=v1.36.0 HELM=v3.22.0 CILIUM_CLI=v0.20.1
RUN for i in 1 2 3; do apt-get update -qq && apt-get install -y -qq --no-install-recommends \
      clang llvm libbpf-dev bpftool iproute2 iptables iputils-ping tcpdump python3 docker-cli && break; sleep 5; done \
 && rm -rf /var/lib/apt/lists/* \
 && ln -s /usr/include/$(uname -m)-linux-gnu/asm /usr/include/asm
RUN set -e; A=$(dpkg --print-architecture); \
    curl -fsSLo /usr/local/bin/kind https://github.com/kubernetes-sigs/kind/releases/download/$KIND/kind-linux-$A; \
    curl -fsSLo /usr/local/bin/kubectl https://dl.k8s.io/release/$KUBECTL/bin/linux/$A/kubectl; \
    curl -fsSL https://get.helm.sh/helm-$HELM-linux-$A.tar.gz | tar xz -C /tmp && mv /tmp/linux-$A/helm /usr/local/bin/; \
    curl -fsSL https://github.com/cilium/cilium-cli/releases/download/$CILIUM_CLI/cilium-linux-$A.tar.gz | tar xz -C /usr/local/bin; \
    chmod +x /usr/local/bin/kind /usr/local/bin/kubectl; \
    kind version; kubectl version --client; helm version --short; cilium version --client
ENV GOFLAGS=-buildvcs=false GOCACHE=/cache/go-build GOMODCACHE=/cache/go-mod
WORKDIR /opt/stand
CMD ["sleep", "infinity"]
