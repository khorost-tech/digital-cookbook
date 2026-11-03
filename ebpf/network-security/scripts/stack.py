"""Единственное место, где стенд трогает Docker: compose, сборка, пространства имён, kind.

Контейнер tools привилегированный, с сокетом Docker хоста: строит пространства
имён snd/rcv для оси XDP/tc и создаёт kind-кластеры для осей Tetragon и Cilium.
"""

import json
import os
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CMDS = ("send", "netsec")


def sh(*argv, check=True, timeout=1800):
    p = subprocess.run(argv, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("%s: rc=%d\n%s" % (" ".join(argv), p.returncode, out[-3000:]))
    return p.returncode, out


def compose(*args, **kw):
    return sh("docker", "compose", *args, **kw)


def up(fresh=False):
    if fresh:
        compose("down", "-v", check=False)
    for _ in range(3):
        rc, out = compose("up", "-d", "--build", "--wait", check=False)
        if rc == 0:
            break
    else:
        raise SystemExit("стек не поднялся:\n" + out[-2000:])
    build()


def tools(*args, check=True, env=None):
    extra = []
    for k, v in (env or {}).items():
        extra += ["-e", "%s=%s" % (k, v)]
    return compose("exec", "-T", *extra, "tools", *args, check=check)


def build():
    script = "set -e; go generate ./cmd/...; " + "; ".join("go build -o bin/%s ./cmd/%s" % (c, c) for c in CMDS)
    tools("sh", "-c", script)


def result(out):
    line = [l for l in out.split("\n") if l.startswith("RESULT ")][-1]
    return json.loads(line[len("RESULT "):])


def netsec(mode, n, block):
    """Свежие пространства имён и один прогон загрузчика в rcv."""
    tools("sh", "scripts/netns.sh")
    _, out = tools("ip", "netns", "exec", "rcv", "/opt/stand/bin/netsec", mode, "veth-rcv", str(n), ",".join(block),
                   check=False)
    return result(out)


KUBECONFIG = "/opt/stand/out/kubeconfig"
CILIUM = "1.20.2"


def kubectl(*args, check=True):
    return tools("kubectl", *args, check=check, env={"KUBECONFIG": KUBECONFIG})


def cluster(kind):
    """kind-кластер стенда заново: cilium — без штатного CNI и kube-proxy, Cilium с
    kubeProxyReplacement; kubeproxy — штатный CNI kind и kube-proxy (iptables)."""
    tools("sh", "-c", "kind delete cluster --name netsec >/dev/null 2>&1; "
                      "kind create cluster --name netsec --config config/kind-%s.yaml --wait 5m && "
                      "(docker network connect kind $(hostname) 2>/dev/null || true) && mkdir -p out && "
                      "kind get kubeconfig --name netsec --internal > out/kubeconfig" % kind)
    if kind == "cilium":
        tools("sh", "-c", "helm repo add cilium https://helm.cilium.io >/dev/null 2>&1; helm repo update >/dev/null; "
                          "helm install cilium cilium/cilium --version %s -n kube-system "
                          "--set kubeProxyReplacement=true --set k8sServiceHost=netsec-control-plane "
                          "--set k8sServicePort=6443 --set ipam.mode=kubernetes --set operator.replicas=1 && "
                          "cilium status --wait --wait-duration 10m" % CILIUM, env={"KUBECONFIG": KUBECONFIG})
    kubectl("wait", "--for=condition=Ready", "nodes", "--all", "--timeout=10m")


def delete_cluster():
    tools("kind", "delete", "cluster", "--name", "netsec", check=False)


def apply(manifest):
    """kubectl apply манифеста со стандартного входа."""
    p = subprocess.run(["docker", "compose", "exec", "-T", "-e", "KUBECONFIG=" + KUBECONFIG, "tools",
                        "kubectl", "apply", "-f", "-"], cwd=HERE, input=manifest.encode(),
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=600)
    if p.returncode != 0:
        raise SystemExit("kubectl apply: " + p.stdout.decode()[-2000:])


def node(name, *args, check=True):
    """Команда в узле kind (контейнер netsec-<name>)."""
    return tools("docker", "exec", "netsec-" + name, *args, check=check)


def node_netns(name, *args, check=True):
    """Команда из tools в сетевом пространстве имён узла kind (bpftool есть только в tools)."""
    _, pid = tools("docker", "inspect", "-f", "{{.State.Pid}}", "netsec-" + name)
    return tools("nsenter", "-t", pid.strip(), "-n", *args, check=check)
