# 06-observability — Prometheus + Grafana + prometheus-nats-exporter, backup/restore

Демо к статье [«Эксплуатация NATS: nats CLI, мониторинг, backup и обновление кластера»](https://khorost.tech/messaging/nats-administration-observability/).

Один узел `nats-server` (образ `nats:2.12`) с включённым JetStream и monitoring-портом (`-m 8222`), рядом — `prometheus-nats-exporter`, Prometheus и Grafana с готовым provisioning (datasource + дашборд). Стенд намеренно на одной ноде: тема этой статьи — не кластеризация (она в [`02-cluster`](../02-cluster)), а инструменты наблюдения и эксплуатации, которые одинаково работают что на одной ноде, что на кластере.

## Запуск

```bash
docker compose up -d
```

Через несколько секунд все четыре сервиса готовы.

## Доступы

| Сервис | URL | Логин |
|---|---|---|
| Grafana | http://localhost:3000 | без логина (anonymous Admin) |
| Prometheus | http://localhost:9090 | — |
| NATS monitoring | http://localhost:8222 | — |
| prometheus-nats-exporter | http://localhost:7777/metrics | — |

Проверка, что monitoring API отвечает:

```bash
curl -s localhost:8222/varz | head -c 200
curl -s localhost:9090/-/healthy
```

## nats CLI и контекст

```bash
nats context save local --server localhost:4222 --select
```

## Grafana provisioning

```
grafana/
  provisioning/
    datasources/   # автоподключение Prometheus как datasource
    dashboards/    # автозагрузка дашбордов из папки
  dashboards/
    nats-observability.json  # дашборд «NATS — Observability demo»
```

Дашборд загружается автоматически — откройте http://localhost:3000 → Dashboards → «NATS — Observability demo». Панели: активные соединения, `in_client_msgs`/`out_client_msgs` (rate, метрики, добавленные в nats-server 2.12.9), `in_client_bytes`/`out_client_bytes` (rate), память/диск JetStream.

## prometheus.yml

Prometheus скрейпит `prometheus-nats-exporter` каждые 5 секунд:

```yaml
scrape_configs:
  - job_name: nats
    static_configs:
      - targets:
          - 'nats-exporter:7777'
```

Экспортёр запущен с флагами `-varz -connz -routez -subz -jsz=streams` — метрики из monitoring endpoints `/varz`, `/connz`, `/routez`, `/subsz`, `/jsz` (значение `-jsz` — список фильтров, здесь `streams`; полный набор задаётся перечислением `streams,consumers,accounts`). Каждая метрика получает префикс `gnatsd_`, например `/varz` → `gnatsd_varz_connections`, `gnatsd_varz_in_client_msgs`.

## Сценарий: backup и restore stream

Создаём stream и публикуем несколько сообщений:

```bash
nats stream add ORDERS --subjects "orders.>" --storage file --retention limits --defaults
nats pub orders.new '{"id":1}'
nats pub orders.new '{"id":2}'
nats stream info ORDERS   # Messages: 2
```

Снимаем бэкап (каталог с `backup.json` и `stream.tar.s2`):

```bash
nats stream backup ORDERS ./orders-backup
```

Имитируем потерю stream (человеческая ошибка, а не падение ноды — от такого RAFT-репликация не защищает):

```bash
nats stream rm ORDERS -f
nats stream ls   # ORDERS отсутствует
```

Восстанавливаем из бэкапа — stream с таким именем не должен существовать на момент restore:

```bash
nats stream restore ./orders-backup
nats stream info ORDERS   # Messages: 2, те же First/Last Sequence, что были до удаления
```

На кластере (например, [`02-cluster`](../02-cluster)) restore умеет сразу поднять stream с другим числом реплик, чем было в бэкапе, через `--replicas`, `--cluster`, `--tag` — полезно при переносе single-node stream на отказоустойчивую топологию. На этом однонодовом стенде `--replicas > 1` не сработает (`replicas > 1 not supported in non-clustered mode`) — это ограничение кластеризации, а не backup/restore.

## Наблюдение через monitoring endpoints

```bash
# JetStream: meta-cluster, streams, consumers
curl -s "localhost:8222/jsz?streams=1&consumers=1" | jq

# Клиентские соединения с подписками
curl -s "localhost:8222/connz?subs=1" | jq

# Здоровье ноды — то же, что используется как gate в rolling upgrade
curl -s localhost:8222/healthz
```

## Разовые проверки через nats CLI

```bash
nats server check jetstream   # health-check в Nagios-совместимом формате, без прав на $SYS
nats server report jetstream  # сводка по кластеру через $SYS — требует системных прав
nats top n1                   # интерактивный top-подобный обзор соединений (через $SYS)
```

`server report` и `nats top` используют системные (`$SYS`) subjects — в этом демо аутентификация не включена, поэтому оба работают без дополнительной настройки. В production-кластере с accounts (ст. 8 серии) для них нужен явный доступ к `$SYS`.

## Остановка

```bash
docker compose down -v
```

`./data` — bind-mount каталог (не именованный volume), `-v` его не удаляет; при необходимости почистите вручную (`rm -rf data`) перед следующим чистым запуском.
