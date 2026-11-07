# Стенд: сервис коротких ссылок от кода до прода

Стенд серии «От кода до прода: сервис с нуля». Один сервис проходит путь от кода до кластера, и на
каждом шаге — опыт с контролем: что ломается, если шаг пропустить, и чем это видно.

- **Сервис** (Go 1.27): API коротких ссылок, редирект, cache-aside в Redis со singleflight,
  публикация кликов в NATS JetStream (ограниченный буфер или синхронно), событие создания ссылки —
  через outbox или напрямую. Конфигурация — только env, проверяется целиком до старта; graceful
  shutdown со снятием готовности; OpenTelemetry включается env.
- **Consumer** (Java 25): durable JetStream `analytics`, идемпотентная агрегация кликов по минутам в
  PostgreSQL, ack после коммита.

## Оси и статьи

| статья | оси | где |
|---|---|---|
| 1. Сервис и его границы | `config`, `shutdown`, `errors`, `cache` | docker compose |
| 2. События без потерь | `broker_down`, `outbox`, `consumer` | docker compose |
| 3. От коммита до кластера | `image`, `reproducible`, `sbom`, `sign`, `scan` | docker, buildx, syft, cosign, trivy |
| | `rollout`, `keda`, `gitops` | k8s-volga, namespace `cookbook-capstone` |
| 4. Под нагрузкой | `trace`, `slo`, `load` | docker compose, otelcol, promtool, k6 |

Гипотезы каждой оси записаны в `expected.json` до прогона; пересмотры (`revision_*`) — до сессий,
с тем, что их вызвало. Результат — `fixtures/checks.jsonl`, строка на ось: пороговые признаки в
`actual`, шумные замеры (время, число находок, ряды) — в полях с `~`.

## Требования

- Docker с buildx, 8 ГБ памяти под Docker, 3 ГБ свободного места.
- Для осей статьи 3 на кластере: `kubectl`, kubeconfig k8s-volga (`STAND_KUBECONFIG`, по умолчанию
  `~/Projects/khorost/kubeconfig-volga`, контекст `admin@k8s-volga`), push в Harbor
  `harbor.khorost.tech`, проект `volga`.

## Подготовка кластера (делает автор)

Учётные данные стенд не вводит и не хранит. Один раз:

```bash
docker login harbor.khorost.tech
kubectl --kubeconfig ~/Projects/khorost/kubeconfig-volga create namespace cookbook-capstone
kubectl --kubeconfig ~/Projects/khorost/kubeconfig-volga -n cookbook-capstone create secret docker-registry harbor-pull \
    --docker-server=harbor.khorost.tech --docker-username=<робот> --docker-password=<токен>
python3 stand/axes_k8s.py install
```

Последняя команда ставит **собственный** ArgoCD v3.4.1 в `cookbook-capstone`:

- из `namespace-install.yaml` с проверкой sha256 (тот же файл, что у стенда `kubernetes/gitops`);
- без CRD и без кластерных ролей — это проверяется до применения, иначе установка не начинается;
- CRD `argoproj.io` в кластере общие и принадлежат боевому ArgoCD (namespace `argocd`) — стенд их
  только использует и не меняет;
- права — Role в своём namespace; область наблюдения — один namespace (`15-cluster-scope.yaml`).

## Запуск

```bash
scripts/run-all.sh                         # все оси, манифест сессии, маркер, тесты, диверсии
python3 scripts/run-checks.py rollout      # одна ось, без записи фикстуры
```

## Что важно знать о кластерной части

- **Образы — `linux/amd64`, по дайджесту, без provenance.** Узлы k8s-volga — amd64, сборка на Mac
  кросс-компилирует Go (`--platform=$BUILDPLATFORM` в Dockerfile), Maven работает без эмуляции.
  Индексы с attestation в проекте volga дважды пропадали через несколько минут после push — стенд
  пушит одиночные манифесты.
- **UID числом.** В distroless пользователь `nonroot` записан именем; kubelet при `runAsNonRoot`
  по имени не может проверить, что это не root, и не запускает контейнер. В манифестах
  `runAsUser: 65532`.
- **Pod Security.** Namespace под `baseline` (ArgoCD v3.x `restricted` не проходит); поды сервиса,
  consumer'а и зависимостей написаны под `restricted`, `warn=restricted` следит за этим.
- **KEDA.** `cooldownPeriod` действует только на переход в 0; снижение 4 → 1 решает HPA, у которого
  окно стабилизации по умолчанию 300 с, — в `ScaledObject` оно задано явно.

## Уборка

```bash
kubectl --kubeconfig ~/Projects/khorost/kubeconfig-volga delete namespace cookbook-capstone
docker compose -f deploy/compose.yml --profile obs down -v
docker volume rm capstone-trivy capstone-gomod
rm -rf out
```

Удаление namespace снимает всё, что стенд создал в кластере, включая его ArgoCD. Кластерных
объектов стенд не создаёт.
