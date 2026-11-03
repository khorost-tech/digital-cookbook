"""Ось 3: балансировка сервисов — Cilium (eBPF) против kube-proxy (iptables).

Кластер kind (control-plane + worker) создаётся заново для каждого варианта:
  kubeproxy — штатный CNI kind и kube-proxy в режиме iptables (контроль);
  cilium    — Cilium 1.20 с kubeProxyReplacement, без kube-proxy: состояние агента
              (cilium-dbg service list) и сама eBPF-карта балансировщика на worker
              (cilium-dbg bpf lb list), пары «ClusterIP — адрес пода» против EndpointSlice.
Нагрузка — N сервисов по M эндпоинтов (pause) в namespace lb; эталон — N и N×M.
Счёт: сервисов и бэкендов в карте балансировщика Cilium, цепочек KUBE-SVC/SEP и
правил KUBE- в nat-таблице узла worker, eBPF-программ на его интерфейсах.

    python3 scripts/run-cilium.py              → оба варианта, фикстура
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import CILIUM, END_MARKER  # noqa: E402

N, M = 20, 3


def workload():
    docs = ["apiVersion: v1\nkind: Namespace\nmetadata: {name: lb}"]
    for i in range(N):
        docs.append("""apiVersion: apps/v1
kind: Deployment
metadata: {name: s%(i)d, namespace: lb}
spec:
  replicas: %(m)d
  selector: {matchLabels: {app: s%(i)d}}
  template:
    metadata: {labels: {app: s%(i)d}}
    spec:
      containers: [{name: pause, image: "registry.k8s.io/pause:3.10", resources: {requests: {cpu: 1m, memory: 4Mi}}}]
---
apiVersion: v1
kind: Service
metadata: {name: s%(i)d, namespace: lb}
spec:
  selector: {app: s%(i)d}
  ports: [{port: 80, targetPort: 8080}]""" % {"i": i, "m": M})
    stack.apply("\n---\n".join(docs))
    stack.kubectl("-n", "lb", "wait", "--for=condition=Available", "deploy", "--all", "--timeout=10m")
    _, svcs = stack.kubectl("-n", "lb", "get", "svc", "-o", "json")
    cluster_ip = {s["metadata"]["name"]: s["spec"]["clusterIP"] for s in json.loads(svcs)["items"]}
    _, eps = stack.kubectl("-n", "lb", "get", "endpointslices", "-o", "json")
    pairs = set()  # (ClusterIP, адрес пода) — эталон для карты балансировщика
    for s in json.loads(eps)["items"]:
        ip = cluster_ip[s["metadata"]["labels"]["kubernetes.io/service-name"]]
        for e in s.get("endpoints") or []:
            for a in e["addresses"]:
                pairs.add((ip, a))
    return set(cluster_ip.values()), pairs


def iptables():
    _, nat = stack.node("worker", "iptables-save", "-t", "nat")
    lines = nat.split("\n")
    return {"kube_rules": sum(1 for l in lines if l.startswith("-A KUBE-")),
            "svc_chains_lb": sum(1 for l in lines if l.startswith(":KUBE-SVC-")) and
            len({l.split()[1] for l in lines if l.startswith("-A KUBE-SVC-") and "lb/s" in l}),
            "sep_chains_lb": len({l.split()[1] for l in lines if l.startswith("-A KUBE-SEP-") and "lb/s" in l})}


def programs():
    _, out = stack.node_netns("worker", "bpftool", "net", "show", "-j")
    progs = []
    for section in json.loads(out):
        for kind in ("xdp", "tc"):
            for p in section.get(kind, []):
                progs.append((p.get("devname"), p.get("kind", kind), p.get("name")))
    return progs


def socket_lb():
    """Подключения программ к cgroup (socket LB): балансировка при connect/sendmsg
    ещё до пакета. Узлы kind делят ядро VM — список ядра видит оба узла."""
    _, progs = stack.tools("bpftool", "prog", "show", "-j")
    names = {p["id"]: p.get("name", "") for p in json.loads(progs)}
    _, links = stack.tools("bpftool", "link", "show", "-j")
    sock = [(l["attach_type"], names.get(l.get("prog_id"), ""), l.get("cgroup_id")) for l in json.loads(links)
            if l.get("type") == "cgroup" and names.get(l.get("prog_id"), "").startswith("cil_sock")]
    return {"socket_lb_links": len(sock), "socket_lb_cgroups": len({s[2] for s in sock}),
            "socket_lb_attach_types": sorted({s[0] for s in sock})}


def measure(name):
    stack.cluster(name)
    ips, pairs = workload()
    row = {"probe": "%d services x %d endpoints" % (N, M), "services": N, "endpoints": N * M,
           "endpointslice_backends": len(pairs)}
    row.update(iptables())
    row.update(socket_lb())
    progs = programs()
    # число программ зависит от раскладки подов по узлам — вне сравнения сессий («~»)
    row["ebpf_programs~"] = len(progs)
    row["has_ebpf_programs"] = bool(progs)
    row["program_names"] = sorted({p[2] for p in progs})
    if name == "cilium":
        _, out = stack.kubectl("-n", "kube-system", "exec", "ds/cilium", "-c", "cilium-agent", "--",
                               "cilium-dbg", "service", "list", "-o", "json")
        svcs = [s for s in json.loads(out) if s["spec"]["frontend-address"]["ip"] in ips]
        row["lb_services"] = len(svcs)
        row["lb_backends"] = sum(len(s["spec"].get("backend-addresses") or []) for s in svcs)
        # сама eBPF-карта балансировщика на узле worker — не состояние агента, а cilium-dbg bpf lb list
        _, pod = stack.kubectl("-n", "kube-system", "get", "pods", "-l", "k8s-app=cilium", "--field-selector",
                               "spec.nodeName=netsec-worker", "-o", "name")
        _, out = stack.kubectl("-n", "kube-system", "exec", pod.strip(), "-c", "cilium-agent", "--",
                               "cilium-dbg", "bpf", "lb", "list", "-o", "json")
        map_services, map_pairs = set(), set()
        for key, vals in json.loads(out).items():
            fe, slot = key.split(" ")[0], int(key.split("(")[-1].rstrip(")"))
            ip, port = fe.split("/")[0].rsplit(":", 1)
            if ip not in ips or port != "80":
                continue
            if slot == 0:
                map_services.add(ip)
            else:
                for v in vals:
                    map_pairs.add((ip, v.split(":")[0]))
        row["map_services"] = len(map_services)
        row["map_backends"] = len(map_pairs)
        row["map_matches_endpointslices"] = map_pairs == pairs
        row["from_container_programs~"] = sum(1 for p in progs if p[2] == "cil_from_container")
        row["outcome"] = "match" if (row["map_services"], row["map_backends"]) == (N, N * M) and \
            row["map_matches_endpointslices"] else "mismatch"
        row["detail"] = ("агент Cilium: сервисов %d, бэкендов %d; eBPF-карта балансировщика на worker: сервисов %d, "
                         "бэкендов %d, пары «ClusterIP — под» %s EndpointSlice (эталон %d и %d); правил KUBE- в nat %d; "
                         "eBPF-программ на интерфейсах узла %d; подключений socket LB к cgroup %d (узлов %d)" % (
                             row["lb_services"], row["lb_backends"], row["map_services"], row["map_backends"],
                             "совпали с" if row["map_matches_endpointslices"] else "разошлись с", N, N * M,
                             row["kube_rules"], row["ebpf_programs~"], row["socket_lb_links"],
                             row["socket_lb_cgroups"]))
    else:
        row["outcome"] = "match" if (row["svc_chains_lb"], row["sep_chains_lb"]) == (N, N * M) else "mismatch"
        row["detail"] = ("цепочек KUBE-SVC %d, KUBE-SEP %d (эталон %d и %d); правил KUBE- в nat %d; "
                         "eBPF-программ на интерфейсах узла %d; подключений socket LB к cgroup %d" % (
                             row["svc_chains_lb"], row["sep_chains_lb"], N, N * M, row["kube_rules"],
                             row["ebpf_programs~"], row["socket_lb_links"]))
    return row


if __name__ == "__main__":
    common.run("cilium", measure, ("kubeproxy", "cilium"), CILIUM, END_MARKER)
