# ebpf/network-security — XDP, tc и Cilium против счёта пакетов и сервисов

Стенд к статье [«eBPF в сети: XDP, tc и Cilium»](https://khorost.tech/infrastructure/ebpf-network-security/).
Один контейнер `tools`: Go 1.27 + `cilium/ebpf` 0.22 (XDP/tc-программа собирается
`bpf2go`), clang, bpftool, iptables, tcpdump, а также kind 0.33, kubectl 1.36,
helm 3.22 и cilium CLI 0.20 — kind-кластеры создаются через сокет Docker хоста.

**Почему `privileged`, `pid: host` и сокет Docker**: загрузка eBPF-программ,
пространства имён сети, `nsenter` в узлы kind и создание самих узлов. «Хост»
для eBPF — ядро VM Docker (на Mac — LinuxKit 7.0, aarch64).

## Что внутри

```
bpf/filter.c       список блокировки в hash-карте; одна логика на XDP и tc ingress, счётчики per-CPU
cmd/send/          отправитель: ровно N UDP-пакетов с каждого адреса 10.99.0.1–4, счёт в RESULT
cmd/netsec/        приёмник и загрузчик: фильтр на veth, счёт по адресам, tcpdump на том же интерфейсе
scripts/netns.sh   пространства имён snd/rcv, соединённые veth — заново на каждый прогон
config/            kind-cilium.yaml (без CNI и kube-proxy), kind-kubeproxy.yaml (контроль)
scripts/           раннеры осей, разбор с контролями, проверки, диверсии
fixtures/          замеры; manifests/ — сводки сессий; RUN-COMPLETE — маркер
expected.json      гипотезы до прогона, факты проб, решения
```

## Как прогнать

Нужны Docker (Linux-ядро с BTF; Docker Desktop подходит, около 2 ГБ памяти на
кластер) и Python 3.8+. Образы Cilium тянутся с quay.io.

```sh
bash scripts/run-all.sh                      # всё разом, маркер последним
FRESH=1 bash scripts/run-all.sh              # то же с пустых томов и новым кластером
python3 scripts/run-filter.py tc             # один вариант
python3 -m unittest discover -s scripts -p "test_*.py"
python3 scripts/sabotage.py
```

После прогона остаётся kind-кластер `netsec` с Cilium:
`docker compose exec tools kind delete cluster --name netsec` — удалить.

## Что замерено

Две сессии полного прогона (вторая — с пустых томов и новым кластером) совпали
по опубликованному (`fixtures/manifests/`).

**Ось 1 — XDP, tc, iptables** (`filter.jsonl`). По 10 000 UDP-пакетов с
четырёх адресов, блокируются два. Без фильтра (контроль) доходят все 40 000.
XDP (native и generic), tc ingress и iptables — от заблокированных 0, от
остальных по 10 000; счётчики программы — 20 000 отброшено, 20 000 пропущено.
tcpdump на приёмнике при XDP отброшенных пакетов не видит, при tc и iptables —
видит все 20 000. Адреса, занесённые в карту на работающей программе,
блокируются без её перезагрузки.

**Ось 2 — Cilium против kube-proxy** (`cilium.jsonl`). 20 сервисов × 3
эндпоинта. kube-proxy (контроль): 20 цепочек `KUBE-SVC`, 60 `KUBE-SEP`, 254
правила `KUBE-` в nat-таблице узла, eBPF-программ на интерфейсах нет. Cilium:
20 сервисов и 60 бэкендов — и по данным агента (`cilium-dbg service list`), и в
самой eBPF-карте балансировщика на worker (`cilium-dbg bpf lb list`): пары
«ClusterIP — адрес пода» совпали с EndpointSlice; правил `KUBE-` — 0,
около семидесяти eBPF-программ на интерфейсах узла (по программе на интерфейс
каждого пода — число зависит от того, сколько подов попало на узел).

**Tetragon** — в стенде без замеров: Tetragon 1.7.1 ставится в кластер и
отдаёт события запуска процессов, сценарии политик не замерялись (решение в
`expected.json`).

## Чего эти числа не значат

- **Скорость не мерилась.** XDP native и generic дали одинаковый счёт; разница
  между ними — в стоимости обработки пакета, а не в результате.
- **Один узел с нагрузкой** — правил iptables на узле в настоящем кластере
  больше: они растут с числом сервисов и эндпоинтов всего кластера.
  Масштабирование не мерилось.
- **kube-proxy — в режиме iptables.** Режим nftables не замерялся.
- **Версии:** cilium/ebpf 0.22.0, kind 0.33.0 (Kubernetes 1.37.0), Cilium
  1.20.2, cilium CLI 0.20.1, helm 3.22.0, Tetragon 1.7.1, Go 1.27.1, ядро
  7.0.12-linuxkit (aarch64).
