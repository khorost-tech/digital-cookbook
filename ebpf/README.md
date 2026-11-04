# eBPF — примеры

Программы в ядре Linux: точки подключения, карты, verifier, CO-RE; наблюдаемость, сеть и рантайм-безопасность на eBPF.

| Стенд | Описание | Статья |
|---|---|---|
| [`fundamentals/`](fundamentals/) | Основы eBPF на cilium/ebpf (Go) и bpftrace против счёта нагрузки: tracepoint, kprobe, fentry, uprobe и uretprobe (ловушки Go — двойной uprobe и падение uretprobe при росте стека), карты под конкуренцией (гонка в hash, per-CPU, потери ring buffer), вердикты verifier на 12 программах, CO-RE на переименованном поле task_struct | [статья](https://khorost.tech/infrastructure/ebpf-fundamentals/) |
| [`observability/`](observability/) | Наблюдаемость через eBPF против эталона генератора: Beyla против OTEL SDK на Go-сервисе (маршруты, статусы, gRPC, SQL, имена маршрутов без шаблона, связь спанов, прогрев после прицепления), eBPF-профилировщик Alloy → Pyroscope против заданной доли CPU (Go 1.26 и 1.27, с символами и без, потеря вызывающего у листовой функции на arm64), цена — счётчики ядра программ Beyla | 🔜 скоро |
| [`network-security/`](network-security/) | eBPF на сетевом пути против счёта пакетов и сервисов: блокировка адресов через XDP (native и generic), tc ingress и iptables на cilium/ebpf (Go) — дошедшие пакеты, счётчики программы, что видит tcpdump, обновление карты на лету; Cilium в kind с заменой kube-proxy против kube-proxy — карта балансировщика, правила iptables, eBPF-программы на интерфейсах | 🔜 скоро |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
