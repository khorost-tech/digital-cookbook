# 04-bench — сценарии `nats bench`

Демо к статье [«Надёжность и производительность NATS: гарантии, репликация и tuning»](https://khorost.tech/messaging/nats-reliability-performance/).

В этом каталоге нет своего `docker-compose.yml` — стенды переиспользуются:

- Core pub/sub и JetStream R1 — узел из [`../01-jetstream`](../01-jetstream);
- JetStream R3 (влияние реплик на latency) — кластер из [`../02-cluster`](../02-cluster).

Все команды и разбор результатов — в [`scenarios.md`](scenarios.md).

## Важная оговорка

Числа в `scenarios.md` — результат реальных прогонов `nats bench` на demo-железе автора (один ноутбук, Docker Desktop, все ноды кластера — контейнеры на одной машине). Это **иллюстрация порядка величин и соотношения R1/R3, а не бенчмарк и не обещание производительности** для вашего окружения. На реальном железе с отдельными нодами, honest-сетью между ними и без соседних контейнеров, конкурирующих за CPU/диск, абсолютные цифры будут другими. Меряйте `nats bench` на своей инфраструктуре и под свою нагрузку — раздел [«Измеряем: nats bench»](https://khorost.tech/messaging/nats-reliability-performance/#bench) статьи объясняет, как.

## Версия

`nats-server 2.12.x` (образ `nats:2.12`, тег без суффикса `-server`), `nats` CLI ([nats-io/natscli](https://github.com/nats-io/natscli)) — синтаксис `nats bench` проверен на CLI `v0.3.0`. Начиная с определённой версии `natscli` команда `nats bench` перестала быть одной командой с флагами `--pub`/`--sub`/`--js` и стала деревом подкоманд: `bench pub`, `bench sub`, `bench js pub sync|async`, `bench js fetch`/`bench js consume`/`bench js ordered`. Все команды ниже — под актуальный синтаксис; если у вас установлен старый `natscli`, обновите его (`nats` CLI ставится отдельно от сервера).

## Быстрый запуск (Core + JetStream R1)

```bash
cd ../01-jetstream
docker compose up -d && sleep 3
```

Сценарии 1 и 2 (Core, JetStream R1) — против этого узла (`localhost:4222`).

```bash
docker compose down -v
```

## Запуск для сценария R3

```bash
cd ../02-cluster
docker compose up -d && sleep 8
```

Сценарий 2 (JetStream R3) и сценарий 3 (pull-consumer) — против этого кластера (`localhost:4222` = нода `n1`, остальные — `4223`/`4224`).

```bash
docker compose down -v
rm -rf data-1 data-2 data-3   # bind-mount каталоги, -v их не удаляет — см. README 02-cluster
```

## Что внутри `scenarios.md`

1. Core pub/sub throughput — верхняя граница без persistence.
2. JetStream publish R1 vs R3 — sync и async publish, влияние репликации на latency записи.
3. Pull-consumer (fetch) — скорость вычитывания подтверждённого backlog.

Для каждого — команда, что она проверяет, и поле с фактическими числами прогона автора (раздел «Результаты прогона автора» в каждом сценарии).
