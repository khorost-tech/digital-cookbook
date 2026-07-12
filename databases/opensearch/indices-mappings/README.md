# OpenSearch: индексы, маппинги и шаблоны

Companion-демо к статье [«OpenSearch: индексы, маппинги и шаблоны»](https://khorost.tech/databases/opensearch-indices-mappings/)
(серия «OpenSearch: глубокое погружение»). Всё проверено на **OpenSearch 3.5.0**.

Один скрипт поднимает одноузловой OpenSearch и показывает жизненный цикл схемы:
dynamic mapping → component/index templates → `_simulate` → `_analyze` → `dynamic: strict`
→ bulk-загрузка → алиасы с ротацией write-индекса.

## Запуск

```bash
./demo.sh
# убрать контейнер после:
docker rm -f osim-demo
```

Требуется Docker. По умолчанию контейнер `osim-demo` на порту `9210`. Переопределить:
`PORT=9300 NAME=my-os ./demo.sh`.

## Состав

| Файл | Что это |
|------|---------|
| `component-templates.json` | component template `logs-common` — переиспользуемый блок полей лог-записи (`ts` date, `level`/`service` keyword, `message` text+keyword, `status` short, `latency_ms` float) |
| `index-template.json` | index template для `app-logs-*`: `composed_of: [logs-common]`, `dynamic: strict`, 1 шард / 0 реплик |
| `sample-docs.ndjson` | демо-документы для `_bulk` |
| `demo.sh` | сценарий: dynamic vs explicit, `_simulate`, `_analyze`, strict-отклонение, алиасы |

## Demo credentials

Пароль `ImDemo#2026` и self-signed TLS (demo-security дистрибутива) — **только для локального
стенда**. За пределами localhost сгенерируйте свой пароль и выпустите настоящие сертификаты
(см. предыдущую статью серии про [установку кластера](https://khorost.tech/infrastructure/opensearch-cluster-ansible/)).

Имена индексов (`app-logs-*`, `service-a`/`service-b`) — обобщённые, не из реальной инфраструктуры.
