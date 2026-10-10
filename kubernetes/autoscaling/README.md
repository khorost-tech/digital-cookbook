# Стенд: автоскейлинг (k8s-volga)

Живые замеры для статьи серии «Kubernetes на практике» про HPA/VPA/KEDA. Этот
файл — каркас: сервис `metricgen`, отдающий управляемую бизнес-метрику
`demo_queue_depth`, и подтверждение, что метрика реально доходит до бэкенда
наблюдаемости кластера. На неё в следующих задачах серии навешиваются HPA
(через Prometheus Adapter) и `ScaledObject` KEDA.

Стенд использует общий namespace `cookbook-k8s` со стендом
[`../resources`](../resources/README.md) (ResourceQuota/LimitRange уже
применены тем стендом).

## Требования к кластеру

- Kubernetes 1.36+.
- `metrics-server` — обязателен для демо «HPA по CPU» (`autoscaling/v2`,
  метрика `type: Resource`, без него HPA держит `cpu: <unknown>/50%` и не
  масштабирует).
- Работающий сбор метрик по конвенции `prometheus.io/scrape` (см. ниже — в
  k8s-volga это vmagent, не kube-prometheus-stack) — нужен только для сквозной
  проверки в демо `metricgen`, HPA по кастомной метрике и KEDA используют
  собственный Prometheus стенда, от кластерного бэкенда не зависят.
- Namespace `cookbook-k8s` с `ResourceQuota`/`LimitRange` — создаётся стендом
  `resources` (`kubernetes/resources/manifests/00-namespace.yaml`).
- Права на создание namespace (`custom-metrics`, `keda`), CRD (`scaledobjects.keda.sh`
  и другие CRD KEDA) и регистрацию `APIService` (`v1beta1.custom.metrics.k8s.io`,
  `v1beta1.external.metrics.k8s.io`) — их регистрируют Prometheus Adapter и KEDA
  при установке helm-чартами.
- Кластер на Talos Linux: к узлам нет SSH и нет прямого доступа к ноде — всё
  наблюдение и все проверки в этом стенде идут через `kubectl` (`exec`, `logs`,
  `get --raw`, `port-forward`), ничего мимо API-сервера.

## Таблица версий

| Компонент | Версия |
|---|---|
| Kubernetes | 1.36.2 |
| golang (образ пода `metricgen`, `go run`) | `golang:1.26` |
| VictoriaMetrics vmagent (сбор метрик в кластере) | v1.106.0 |
| `registry.k8s.io/hpa-example` (демо `php-apache`, HPA по CPU) | без явного тега (официальный демо-образ проекта Kubernetes) |
| `busybox` (демо `load-gen`, HPA по CPU; демо `consumer`, HPA по кастомной метрике) | 1.37 |
| `prom/prometheus` (собственный Prometheus стенда, ns `cookbook-k8s`) | `v3.13.1` (запинован тегом релиза в манифесте, не `latest`; на момент установки `v3.13.1` — актуальный стабильный релиз) |
| Prometheus Adapter (чарт `prometheus-community/prometheus-adapter`) | чарт `5.3.0`, образ `registry.k8s.io/prometheus-adapter/prometheus-adapter:v0.12.0` |
| KEDA (чарт `kedacore/keda`) | чарт `2.20.1`, образы `ghcr.io/kedacore/keda:2.20.1`, `ghcr.io/kedacore/keda-metrics-apiserver:2.20.1`, `ghcr.io/kedacore/keda-admission-webhooks:2.20.1` (последняя на момент установки; `helm search repo kedacore/keda --versions`) |

## Почему go run из ConfigMap, а не образ

Архитектурное решение: **сервис `metricgen` не собирается в Docker-образ и не
пушится в registry**. Исходник (`cmd/metricgen/main.go`) на чистой стандартной
библиотеке Go — экспозиция метрик в формате Prometheus реализована без
клиентской библиотеки, только `net/http`+`fmt`+`strconv`+`sync/atomic`+`os`+`log`.
Живой стенд кладёт исходник в `ConfigMap metricgen-src`, монтирует его в под на
`/src` и запускает `go run /src/main.go` на стоковом образе `golang:1.26`
(`manifests/20-metricgen.yaml`). Никакой сборки, никакого registry, никакой
сети к модульному прокси (зависимостей нет — `go run` одного файла не требует
`go.mod`).

`Dockerfile` в стенде — **продуктовый путь для читателя**: как выглядит
нормальная multi-stage сборка того же сервиса (статический бинарник,
`distroless/static:nonroot`, без запуска компилятора в проде). Он не
собирается и не используется живым стендом — это намеренно, показывает
разницу между «демо без registry» и «как деплоят по-настоящему».

Плата за `go run` — компиляция на каждом старте пода: `readinessProbe` и
`livenessProbe` заданы с запасом по `initialDelaySeconds` (45с/60с), под
монтирует `emptyDir` в `/tmp` для `GOCACHE`/`GOPATH`/`HOME` (образ `golang`
пишет туда при компиляции), `resources.limits.memory` — 512Mi (у компилятора
Go заметный аппетит к памяти на холодной сборке).

## Скрейп в Prometheus/VictoriaMetrics — отклонение от исходного плана

План задачи предполагал kube-prometheus-stack (Prometheus Operator, CRD
`ServiceMonitor`, локальный Prometheus в `monitoring` со своим Service на
9090). **Разведка (обязательный шаг перед манифестом) показала, что этого в
кластере k8s-volga нет:**

```
$ kubectl -n monitoring get all
kube-state-metrics               (Deployment, Service :8080)
vmagent-victoria-metrics-agent   (Deployment)
$ kubectl get crd | grep -iE "monitoring|prometheus|coreos"
# пусто — Prometheus Operator не установлен, CRD ServiceMonitor не существует
```

Вместо этого namespace `monitoring` держит **VictoriaMetrics vmagent**
(Helm-чарт `victoria-metrics-agent`), который скрейпит по статическому
`scrape.yml` (ConfigMap `vmagent-victoria-metrics-agent-config`) и
remote-write'ит наружу кластера, во внешний VictoriaMetrics
(`--remoteWrite.url=http://192.168.71.83:8429/api/v1/write`). В `scrape.yml`
есть job `kubernetes-pods` — `kubernetes_sd_configs: role: pod` **без
ограничения по namespace или лейблу**, отбирающий поды по трём аннотациям:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"      # опционально в конвенции, но проставлен явно
prometheus.io/path: "/metrics"
```

Это рабочий эквивалент `ServiceMonitor` в этом кластере — конфигурацию
`vmagent` и что-либо в ns `monitoring` мы не трогали (по условию задачи), и
лейбл на namespace `cookbook-k8s` вешать не понадобилось: job `kubernetes-pods`
кластерный, без namespace-селектора. Поэтому `manifests/20-metricgen.yaml`
вместо CRD `ServiceMonitor` вешает три аннотации на `spec.template.metadata`
Deployment `metricgen` — этого достаточно.

**Куда реально доходят метрики (для проверки и для задач B2/B3):**
`vmagent` в `monitoring` (порт 8429, вход remote-write) — это не конечная
точка для запросов, `/api/v1/query` там не поддерживается (это агент, не
хранилище). Данные оттуда улетают дальше и оказываются доступны для чтения на
том же хосте на порту 8428 — это отдельный **VictoriaMetrics vmsingle**
(Prometheus-совместимый `/api/v1/query`), уже содержащий метрики этого
кластера (`cluster="k8s-volga"`, видны `kube-state-metrics`,
`kubernetes-cadvisor` и т.д.). Это внешний, отдельно управляемый компонент —
не часть `monitoring` в k8s-volga, менять его не пытались, только читали.

**URL для запросов (нужен задачам B2 — Prometheus Adapter, и B3 — KEDA):**

```
http://192.168.71.83:8428
```

(Prometheus-совместимый HTTP API: `GET /api/v1/query?query=...`). Это НЕ
`<svc>.monitoring.svc:9090`, как предполагал исходный план — в кластере такого
Service нет, и запрашивать метрики из подов кластера, скорее всего, придётся
по этому внешнему адресу, а не через in-cluster DNS-имя. B2/B3 должны это
учесть при настройке Prometheus Adapter (его `url`/`prometheus-adapter`
конфиг).

Живое подтверждение (после `/push?n=7` на `metricgen`, ожидание ~60с на
дискавери+скрейп):

```
$ curl -s --get --data-urlencode 'query=demo_queue_depth' http://192.168.71.83:8428/api/v1/query
{"status":"success","data":{"resultType":"vector","result":[{"metric":{
  "__name__":"demo_queue_depth","cluster":"k8s-volga",
  "instance":"10.244.4.132:8080","job":"kubernetes-pods"},
  "value":[1784765873,"7"]}]}, ...}
```

Значение `7` совпадает с тем, что было выставлено через `/push?n=7` — метрика
реально дошла от пода до бэкенда наблюдаемости. Обратите внимание: лейблов
`namespace=`/`pod=` в результате нет — конфиг `vmagent` в этом кластере не
маппит `__meta_kubernetes_namespace`/`__meta_kubernetes_pod_name` в лейблы для
job `kubernetes-pods` (только `instance` = IP пода:порт, `job`, `cluster`).
Полный вывод — `fixtures/20-metricgen.txt`.

## Демо

- **`demo_queue_depth`** (`manifests/20-metricgen.yaml`, `fixtures/20-metricgen.txt`) —
  под `metricgen` (1 реплика) отдаёт `GET /metrics` в формате экспозиции
  Prometheus, `GET /push?n=<int>` выставляет значение, `GET /work` жжёт CPU
  детерминированным busy-loop (пригодится для демо на ресурсных метриках),
  `GET /healthz` — проба готовности/живости. Метрика подтверждена живьём и в
  `curl` к поду через port-forward, и запросом к внешнему VictoriaMetrics.

- **HPA по CPU** (`manifests/10-hpa-cpu.yaml`, `manifests/11-load-gen.yaml`,
  `fixtures/10-hpa-cpu.txt`) — классический сценарий `HorizontalPodAutoscaler`
  на ресурсной метрике `cpu` через `metrics-server` (Prometheus здесь не
  участвует). `Deployment php-apache` (`registry.k8s.io/hpa-example`,
  `requests.cpu: 200m`) с `HPA` (`minReplicas: 1`, `maxReplicas: 10`, target
  `averageUtilization: 50`, `behavior.scaleDown.stabilizationWindowSeconds:
  60`), под `load-gen` (`busybox:1.37`) долбит его в бесконечном цикле `wget`.

  Живой ряд снят двумя фазами по ~6 минут на кластере k8s-volga (полностью в
  `fixtures/10-hpa-cpu.txt`, точки каждые 30с с отметкой времени):

  **Фаза 1 (нагрузка включена).** До старта `load-gen` — `cpu: 0%/50%`, 1
  реплика. Через 30с после старта CPU уже `250%/50%` (мгновенный всплеск —
  один под захлёбывается запросами быстрее, чем HPA успевает среагировать),
  HPA сразу поднимает реплики до 3. На 60-й секунде — `10/10` (максимум
  `maxReplicas`, дошли за минуту). Дальше нагрузка размазывается по 10 подам,
  утилизация проседает ниже target, и к 150-й секунде HPA откатывает на 1 под
  вниз, до 9 реплик — на этом уровне система стабилизируется и держится весь
  оставшийся 6-минутный интервал, но не сразу выходит на target: CPU идёт
  `42% → 45% → 46% → 46% → 46% → 47% → 49% → 46%` (t=150s..360s), то есть
  реально колеблется в диапазоне `42–49%` вокруг target 50%, а не сразу
  устаканивается у верхней границы. **Пик — 10 реплик, достигнут за 60с;
  устойчивый уровень под постоянной нагрузкой — 9 реплик.**

  **Фаза 2 (нагрузка снята, `kubectl delete pod load-gen`).** Метрика гаснет
  не сразу: `47% → 31% → 0%` за первые 90с (используется скользящее среднее
  метрики за последний интервал). Реплики начинают падать только после
  обнуления CPU: `9 → 6` на 120-й секунде, `6 → 1` на 150-й. **Возврат к 1
  реплике занял 150с** — это на 90с больше, чем
  `stabilizationWindowSeconds: 60`: окно стабилизации — это не «подождать 60с
  и обнулить», а «за последние 60с взять максимум из рекомендованных
  значений» — HPA снижает реплики пошагово, на каждом цикле пересчёта
  ограничиваясь этим максимумом, поэтому полный спуск с 9 до 1 растягивается
  на несколько циклов и занимает больше, чем сама величина окна.

## HPA по кастомной метрике

Демо №2 серии: масштабирование не по ресурсам (CPU/память), а по
бизнес-метрике `demo_queue_depth`. Цепочка:

```
metricgen (demo_queue_depth) → Prometheus (cookbook-k8s) → Prometheus Adapter
  (custom-metrics) → custom.metrics.k8s.io → HPA → consumer
```

### Почему собственный Prometheus, а не внешний бэкенд кластера

В k8s-volga нет Prometheus Operator; метрики собирает VictoriaMetrics
vmagent, который remote-write'ит наружу кластера, во внешний
`192.168.71.83:8428` (см. «Скрейп в Prometheus/VictoriaMetrics» выше). Этот
внешний хост — не часть стенда: читатель, воспроизводящий пример у себя, его
не имеет, и это неуправляемая инфраструктура, на которую нельзя полагаться в
демонстрации Prometheus Adapter. Поэтому `manifests/15-prometheus.yaml`
разворачивает **отдельный Prometheus внутри `cookbook-k8s`** (образ
`prom/prometheus:v3.13.1`, `emptyDir` для TSDB, `--storage.tsdb.retention.time=1h`
— стендовая конфигурация, не прод). RBAC — `ServiceAccount` + `Role` +
`RoleBinding`, только в `cookbook-k8s`, только `get/list/watch` на `pods`; ns
`monitoring` не тронут вообще. Scrape-конфиг (`ConfigMap prometheus-config`)
использует `kubernetes_sd_configs` `role: pod` с `namespaces.names:
[cookbook-k8s]` и ту же конвенцию аннотаций `prometheus.io/scrape|port|path`,
что и `metricgen`. Ключевой relabel, без которого Object-метрика не
соберётся: лейбл пода `app` переносится в лейбл результата `service`
(`__meta_kubernetes_pod_label_app` → `service`) — Prometheus Adapter
ассоциирует custom-метрику с k8s-ресурсом `Service` именно по этому лейблу.

Живая проверка (после `push?n=7`, ~10–15с на дискавери + скрейп с интервалом
`10s`):

```
$ curl -s --get --data-urlencode 'query=demo_queue_depth' http://localhost:19090/api/v1/query
{"status":"success","data":{"resultType":"vector","result":[{"metric":{
  "__name__":"demo_queue_depth","instance":"10.244.4.132:8080",
  "job":"cookbook-k8s-pods","namespace":"cookbook-k8s",
  "pod":"metricgen-54f444698c-mcvkb","service":"metricgen"},
  "value":[1784785482.228,"7"]}]}}
```

В отличие от внешнего VictoriaMetrics (см. «Скрейп…» выше, там лейблов
`namespace`/`pod`/`service` не было вовсе — конфиг `vmagent` их не
проставляет), собственный Prometheus стенда возвращает все три нужных
Adapter'у лейбла.

### Установка Prometheus Adapter (ns `custom-metrics`)

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl create namespace custom-metrics
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install prometheus-adapter prometheus-community/prometheus-adapter \
  -n custom-metrics --version 5.3.0 \
  -f manifests/prometheus-adapter-values.yaml
```

Версии (резолвлены на момент установки, `helm search repo
prometheus-community/prometheus-adapter --versions`): чарт `5.3.0`, образ
`registry.k8s.io/prometheus-adapter/prometheus-adapter:v0.12.0` (appVersion
чарта). Значения — `manifests/prometheus-adapter-values.yaml`:
`prometheus.url=http://prometheus.cookbook-k8s.svc`, `prometheus.port=9090`,
`rules.default: false` (встроенные default-правила по cpu/memory отключены —
не нужны демо, только шумели бы рядом с `demo_queue_depth`), `rules.external:
[]` (**⚠️ важно**: в кластере может быть зарегистрирован только один
`v1beta1.external.metrics.k8s.io` — этот слот займёт KEDA в задаче B3, поэтому
здесь адаптер регистрирует только `custom.metrics.k8s.io`, external-правила
явно пустые), и единственное `rules.custom` — правило для
`demo_queue_depth`, ассоциирующее метрику с ресурсами `namespace`, `service`
и `pod`.

Проверка регистрации (в Git Bash на Windows — `kubectl get --raw` требует
`MSYS_NO_PATHCONV=1`, иначе MSYS переписывает `/apis/...` в оконный путь и
`kubectl` получает 404 от несуществующего локального файла, а не от API):

```bash
$ kubectl get apiservices | grep -i metrics
v1beta1.custom.metrics.k8s.io     custom-metrics/prometheus-adapter   True   ...
v1beta1.metrics.k8s.io            kube-system/metrics-server          True   ...
# v1beta1.external.metrics.k8s.io отсутствует — слот свободен для KEDA (B3)

$ MSYS_NO_PATHCONV=1 kubectl get --raw \
  "/apis/custom.metrics.k8s.io/v1beta1/namespaces/cookbook-k8s/services/metricgen/demo_queue_depth"
{"kind":"MetricValueList","apiVersion":"custom.metrics.k8s.io/v1beta1","metadata":{},
 "items":[{"describedObject":{"kind":"Service","namespace":"cookbook-k8s","name":"metricgen",
 "apiVersion":"/v1"},"metricName":"demo_queue_depth","timestamp":"2026-07-23T05:47:22Z",
 "value":"7","selector":null}]}
```

Значение `7` совпадает с тем, что было выставлено `push?n=7` — кастомная
метрика реально дошла от пода `metricgen` до `custom.metrics.k8s.io` через
собственный Prometheus и Adapter.

### Почему масштабируем `consumer`, а не сам `metricgen`

`demo_queue_depth` — состояние **в памяти процесса** `metricgen`
(`atomic.Int64` в `cmd/metricgen/main.go`), не разделяемое между репликами.
Если бы HPA масштабировал сам `metricgen`, каждая новая реплика стартовала
бы с нулевым значением метрики, среднее по подам мгновенно проседало бы, HPA
реагировал бы обратным сокращением — классический флаппинг. Поэтому
`metricgen` остаётся источником метрики в 1 реплике (не участвует в
скейлинге), а `manifests/21-hpa-custom.yaml` заводит отдельный дешёвый
`Deployment consumer` (`busybox:1.37`, `sleep infinity`, `requests: 50m/32Mi`,
`limits: 100m/64Mi`) — вот его и масштабирует `HorizontalPodAutoscaler` с
метрикой типа `Object`, описывающей `Service metricgen`, `target:
{type: AverageValue, averageValue: "5"}`. Семантика: HPA берёт значение
метрики объекта (`demo_queue_depth` целиком, не per-pod) и делит на текущее
число реплик `consumer`, сравнивая с target `5` — desiredReplicas =
`ceil(demo_queue_depth / 5)`.

### Живой ряд (снят на k8s-volga, `fixtures/21-hpa-custom.txt`)

HPA `consumer` (`minReplicas: 1`, `maxReplicas: 8`, `behavior` не
переопределён — действуют дефолты Kubernetes: быстрый scaleUp,
`scaleDown.stabilizationWindowSeconds: 300`).

**Фаза 1 (`curl "http://localhost:18080/push?n=40"`).** `ceil(40/5)=8` —
упор в `maxReplicas`. К первому же замеру (**t=30s**) `REPLICAS` уже
показывал `8/8` — промежуточные шаги роста (`1→2→4→8`) не снимались: HPA
удалили сразу после демо (см. «HPA `consumer` удалён после демо» ниже),
события не сохранялись, зафиксирован только итог на t=30s. Из этого выводимо
только то, что рост уложился быстрее интервала опроса 30с — правдоподобно,
учитывая, что дефолтная scaleUp-политика Kubernetes разрешает удвоение
реплик каждые ~15с без ограничения окном стабилизации (стабилизация есть
только на scale-down), поэтому пик достигается быстрее, чем в демо
HPA-по-CPU (там — за 60с). Держалось `8/8` весь снятый интервал `30..300s`
(10 точек подряд по 30с).

**Фаза 2 (`curl "http://localhost:18080/push?n=0"`).** `TARGETS` упал в
`0/5` мгновенно (t=30s) — в отличие от CPU-утилизации, `demo_queue_depth` не
скользящее среднее, а просто текущее значение gauge. Но `REPLICAS` оставался
`8` **весь `stabilizationWindowSeconds=300s`** (дефолт — в HPA он явно не
переопределён, см. `manifests/21-hpa-custom.yaml`), и обвалился до `1`
**одним шагом ровно на t=300s** — не постепенно, как в демо HPA-по-CPU (там
метрика сама затухала несколько циклов подряд, и спуск растягивался). Здесь
метрика была уже нулевой все 300с окна, поэтому по его истечении HPA сразу
применил актуальный расчёт `ceil(0/5)=0 → minReplicas=1`. Стабильно `1`
реплика держалась ещё 150с после (`t=300..450s`, 6 точек подряд).

**Итого: пик — 8 реплик за 30с; возврат к 1 реплике — за 300с после снятия
нагрузки (ровно по окну стабилизации, без растянутого постепенного спуска).**
Полный ряд с таймингами каждые 30с — `fixtures/21-hpa-custom.txt`.

### HPA `consumer` удалён после демо

`HorizontalPodAutoscaler consumer` снят после снятия ряда — в задаче B3 его
место на `Deployment consumer` займёт `ScaledObject` KEDA, а два
автоскейлера на одном `scaleTargetRef` конфликтуют. `Deployment consumer`,
`metricgen`, собственный Prometheus (`manifests/15-prometheus.yaml`) и
Prometheus Adapter (`ns custom-metrics`) остаются развёрнутыми — нужны
задаче B3.

## KEDA и scale-to-zero

Демо №3 серии, финальное: то, чего обычный HPA не умеет в принципе — уйти в
**ноль** реплик по внешнему сигналу и подняться обратно. Цепочка та же
`metricgen → Prometheus (cookbook-k8s) → consumer`, но вместо Adapter'а+HPA
теперь KEDA сам ходит в Prometheus по PromQL и сам решает, сколько реплик
нужно `Deployment consumer`.

### Что KEDA добавляет поверх HPA

`HorizontalPodAutoscaler` (`autoscaling/v2`) по спецификации Kubernetes не
может опуститься ниже `minReplicas: 1` — при `minReplicas: 0` объект будет
отклонён валидацией API (либо, в зависимости от версии, просто никогда не
уйдёт ниже 1: масштабирование до нуля через ядро `autoscaling/v2` не
предусмотрено). KEDA не заменяет HPA — она **создаёт HPA сама**, "под
капотом" (`kubectl -n cookbook-k8s get hpa keda-hpa-consumer` после `apply`
покажет обычный `HorizontalPodAutoscaler`, управляемый оператором KEDA), и
отвечает за него **external-метрикой** через собственный
`keda-operator-metrics-apiserver` (API `external.metrics.k8s.io`). Всё,
что делает "обычный" HPA (`min..max`, шаги масштабирования, `TARGETS`) —
делает и здесь, тем же встроенным HPA-контроллером Kubernetes. Разница —
именно в нуле: пока `minReplicaCount: 0`, KEDA **временно снимает HPA с
управления** ниже минимума в 1, которое требует ядро Kubernetes, и сама,
своим контроллером, ставит `Deployment.spec.replicas = 0` — а когда триггер
снова активен, восстанавливает нормальный HPA-путь. Отсюда и `ACTIVE` в
`kubectl get scaledobject` — статус триггера (сработал/не сработал),
отдельно от `READY` (сам `ScaledObject` корректен и обслуживается).

### Почему источник метрики (`metricgen`) — отдельный от скейлимого объекта (`consumer`)

Тот же принцип развязки, что и в HPA по кастомной метрике (см. выше), но
здесь он не просто желателен — он **обязателен для scale-to-zero**.
`demo_queue_depth` отдаёт `metricgen` — под, который KEDA **не трогает**, он
остаётся в 1 реплике постоянно. Если бы `ScaledObject` целился в тот же под,
что отдаёт метрику (то есть `metricgen` масштабировал бы сам себя), то на
уходе в ноль реплик исчез бы и единственный источник ряда
`demo_queue_depth` в Prometheus. PromQL-запрос `max(demo_queue_depth{...})`
вернул бы пустой результат (`empty vector`) — не "0", а **отсутствие
метрики вообще**. KEDA не может отличить "метрика говорит 0" от "метрики
нет" по умолчанию (без `ignoreNullValues`/`activationThreshold` под пустой
ряд), и главное — даже если бы отличила, поднимать нечему: сигнала для
`pollingInterval` попросту не существует, пока не поднимется хотя бы одна
реплика, а поднимать её нечем, потому что нет сигнала. Замкнутый круг,
из которого нет выхода без внешнего толчка (ручной `kubectl scale` в обход
KEDA). Развязка "источник метрики (`metricgen`, 1 реплика, всегда жив) —
скейлимый объект (`consumer`, может уйти в 0)" разрывает этот круг:
`metricgen` продолжает отдавать `demo_queue_depth` независимо от того,
сколько реплик у `consumer` — 0 или 8, поэтому у KEDA всегда есть, что
опрашивать, и она способна поднять `consumer` обратно из нуля.

### Установка KEDA (ns `keda`)

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl create namespace keda
helm repo add kedacore https://kedacore.github.io/charts
helm repo update kedacore
helm install keda kedacore/keda -n keda --version 2.20.1
kubectl -n keda rollout status deploy/keda-operator --timeout=120s
kubectl -n keda rollout status deploy/keda-operator-metrics-apiserver --timeout=120s
kubectl -n keda get deploy
kubectl get crd | grep keda
kubectl get apiservices | grep external.metrics
```

Версия резолвлена на момент установки (`helm search repo kedacore/keda
--versions`, самая свежая строка) — таблица версий выше. `helm install`
поднимает три `Deployment`: `keda-operator` (контроллер CRD
`ScaledObject`/`ScaledJob`), `keda-operator-metrics-apiserver` (реализует
`external.metrics.k8s.io`, регистрируется как `APIService`) и
`keda-admission-webhooks` (валидация `ScaledObject` при `apply`).
Все три ушли в `Running`/`1/1` в течение ~30–50с после `helm install` (на
k8s-volga: `keda-admission-webhooks` первые секунды `0/1` — ждёт TLS-сертификат
из `Secret kedaorg-certs`, который сама же и создаёт при старте
`cert-rotation`, затем контейнер стартует). `keda-operator` перезапустился
один раз в первые секунды жизни (`cert-rotation: Secrets have been updated;
exiting so pod can be restarted`) — штатное поведение при первом запуске
(ротация TLS-сертификата вебхуков), не сбой; после рестарта — стабилен.
При запуске оператор пишет предупреждение `KEDA 2.20.1 hasn't been tested
on Kubernetes v1.36.2` — не блокирует работу, версия K8s новее матрицы
совместимости релиза KEDA на момент установки.

Проверка (после rollout): CRD `scaledobjects.keda.sh` — есть (вместе с
`scaledjobs.keda.sh`, `triggerauthentications.keda.sh` и др.); APIService
`v1beta1.external.metrics.k8s.io` зарегистрирован **KEDA**
(`keda/keda-operator-metrics-apiserver`) и `Available: True` — слот был
свободен (см. «Установка Prometheus Adapter» выше, Adapter сознательно не
регистрировал `rules.external`), конфликта со слотом
`v1beta1.custom.metrics.k8s.io` (занят `custom-metrics/prometheus-adapter`,
остаётся неизменным) нет — это два разных API-slot'а, HPA от B2 использовал
`custom.metrics.k8s.io`, KEDA использует `external.metrics.k8s.io`.

### Манифест `manifests/30-keda-scaledobject.yaml`

`scaleTargetRef.name: consumer`, `minReplicaCount: 0`, `maxReplicaCount: 8`
(тот же диапазон, что был у HPA в B2), `cooldownPeriod: 60` (сколько ждать
после деактивации триггера перед уходом в 0), `pollingInterval: 15` (как
часто опрашивать Prometheus), триггер `type: prometheus` — `serverAddress`
на собственный Prometheus стенда, `query: max(demo_queue_depth{namespace=
"cookbook-k8s"})`, `threshold: "5"` (тот же target, что был у HPA:
`ceil(значение / 5)` реплик).

**Отклонение от исходного черновика триггера, проверено живьём на
KEDA 2.20.1.** В черновике задачи в `metadata` триггера был ключ
`metricName: demo_queue_depth`. Проверка по факту (`kubectl describe
scaledobject`, сверено с [документацией prometheus-скейлера
2.20](https://keda.sh/docs/2.20/scalers/prometheus/)) показала: это поле
там **не задокументировано и не используется** — движок его молча
игнорирует. Имя external-метрики KEDA генерирует сам, по шаблону
`s<индекс триггера>-<тип>` (здесь — `s0-prometheus`, видно в `kubectl
describe scaledobject consumer` → `External Metric Names`), и это имя не
меняется ни через `metadata.metricName`, ни через top-level
`triggers[].name` (последний — идентификатор триггера для формул scaling
modifiers, а не имя метрики; проверено экспериментально: с полем, без
поля и с обоими вариантами внешнее имя метрики оставалось `s0-prometheus`).
В закоммиченном манифесте `metadata.metricName` убран как мёртвое поле;
`triggers[].name: demo-queue-depth` оставлен читаемым идентификатором
триггера, не более.

### Живой ряд: scale-to-zero → scale-from-zero → повторный scale-to-zero

Снято на k8s-volga, `fixtures/30-keda-scale-to-zero.txt` (полные снимки
`kubectl get scaledobject`/`get hpa`/`get deploy consumer` с отметками
времени UTC). Порядок — как в демонстрации: сначала опустить метрику,
дождаться нуля, затем поднять и показать подъём, затем снова в ноль.

1. **N→0 (сразу при `apply`).** Метрика `demo_queue_depth` была опущена в
   `0` (`push?n=0`) ещё **до** применения `ScaledObject`. При первом же
   снимке после `apply` (`06:20:17Z`, **+43с** от `apply` в `06:19:34Z` —
   совпадает с `AGE 43s/42s` в этом же снимке) `Ready: True`, `Active:
   False`, HPA `keda-hpa-consumer` уже `REPLICAS: 0`, `Deployment consumer`
   — `0/0` (до этого была 1 реплика, оставшаяся от исходного состояния
   стенда). KEDA не ждала `cooldownPeriod` — деактивация видна уже в этом
   первом снимке (триггер был неактивен ещё до первого `reconcile`, потому
   что метрика была опущена в 0 заранее, до `apply`). Отдельный вывод
   события `KEDAScaleTargetDeactivated` в фикстуре не сохранён — этот вывод
   сделан по состояниям `Active`/`REPLICAS`/`READY` в снимках, а не по
   журналу событий `kubectl describe`/`get events`.
2. **0→N.** `push?n=30` в `06:20:26Z`. Первые `Ready`-поды `consumer`
   (`4/4`) — уже в `06:20:45Z` (**+19с**). Полная сходимость к целевым
   `6/6` (`ceil(30/5)=6`) — уже к `06:21:05Z` (**+39с** от `push`): это
   первый снимок, где и HPA `REPLICAS`, и `Deployment` `READY/AVAILABLE`
   оба показывают `6/6` (HPA-цикл переоценивает метрику раз в ~15с,
   поэтому рост идёт ступенями, а не мгновенно всеми 6 разом). Все
   снимки с `06:21:05Z` по `06:22:23Z` (78с подряд) показывают то же
   `6/6` — точка `+39с` не случайный выброс, а устойчиво удерживаемая
   цель; более поздние снимки этой же точки ничего не добавляют, поэтому
   выбрано первое достижение `6/6`, а не более позднее. **Цена холодного
   старта здесь — низкая**:
   `consumer` использует `busybox:1.37`, образ уже в кеше узла (тот же
   образ гонялся в демо HPA по CPU и по кастомной метрике), контейнер —
   `sleep infinity` без инициализации; основное время уходит не на старт
   пода, а на цикл `pollingInterval` KEDA + шаги HPA-алгоритма. Для
   сервиса с тяжёлым стартом (миграции, прогрев кеша, `readinessProbe` с
   заметным `initialDelaySeconds`, как у самого `metricgen` — см. «Почему
   go run из ConfigMap» выше, там 45–60с) цена холодного старта
   scale-from-zero была бы на порядок выше — это компонент времени
   `pollingInterval + время запуска контейнера + readiness`, и в проде для
   задержко-чувствительной нагрузки это стоит явно взвешивать против
   экономии ресурсов в 0 реплик.
3. **N→0 повторно (проверка `cooldownPeriod`).** `push?n=0` в
   `06:22:59Z`. Триггер `Active: False` — уже в снимке `06:23:08Z`
   (**+9с**, один цикл поллинга). Реальный уход `Deployment consumer` с
   `6/6` до `0/0` — в снимке `06:23:55Z` (**+56с от `push`, ~47с от
   момента `Active: False`**). Согласуется с `cooldownPeriod: 60`
   (заданным в манифесте) — с поправкой на грануляцию снятых снимков
   (шаг ~15–16с) и на то, что реальный отсчёт cooldown стартует чуть
   раньше первого пойманного снимка с `Active: False`.

**Итог: реальные переходы `N(1)→0`, `0→N(6)` и `N(6)→0` зафиксированы с
таймингами, без подгонки.** Полный ряд, включая промежуточные снимки каждые
~15с и события `keda-operator` — `fixtures/30-keda-scale-to-zero.txt`.

### `ScaledObject` снят после демо

Как и `HorizontalPodAutoscaler` в B2, `ScaledObject consumer` снимается
после снятия ряда (`kubectl -n cookbook-k8s delete scaledobject consumer`)
— вместе с ним автоматически исчезает и `keda-hpa-consumer` (KEDA
управляет жизненным циклом созданного ей HPA). `Deployment consumer`
остаётся в кластере на `0/0` (последнее состояние, в которое его поставила
KEDA перед удалением триггера) — не восстанавливается автоматически до
исходной 1 реплики. Оператор **KEDA** (как и Prometheus Adapter, VPA)
остаётся развёрнутым — жизненный цикл операторов отдельный от демо-объектов
стенда, снимается вручную (`helm uninstall keda -n keda && kubectl delete
namespace keda`) или в финальном teardown серии (задача B4).

## Windows/Git Bash

Обе команды ниже требуют `MSYS_NO_PATHCONV=1` перед `kubectl` в Git Bash: MSYS
переписывает любой аргумент, похожий на абсолютный Unix-путь, в оконный путь
(`C:/Program Files/Git/...`), `kubectl` получает не тот путь и API-сервер в
ответ на несуществующий локальный путь отдаёт ложный 404 — не потому, что
ресурса нет, а потому что запрос вообще ушёл не туда:

- `kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta1/..."` — используется
  ниже для проверки регистрации кастомной метрики (`/apis/...` выглядит как
  абсолютный путь).
- `kubectl exec <под> -- cat /sys/fs/cgroup/...` — если решите заглянуть в
  реальные cgroup-лимиты пода изнутри (в этом стенде не используется, но тот
  же MSYS-путь `/sys/...` в аргументе после `--` ломается тем же образом; live
  пример — стенд `resources`, `fixtures/20-cpu-throttle.txt`).

## Порядок запуска

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl config current-context   # должен быть admin@k8s-volga

# namespace/ResourceQuota/LimitRange — если ещё не применены стендом resources
../resources/apply.sh manifests/00-namespace.yaml   # см. kubernetes/resources

./apply.sh manifests/20-metricgen.yaml
kubectl -n cookbook-k8s rollout status deploy/metricgen --timeout=300s

kubectl -n cookbook-k8s port-forward svc/metricgen 18080:8080 &
sleep 3
curl -s "http://localhost:18080/push?n=7"
curl -s http://localhost:18080/metrics | grep demo_queue_depth

# подождать ~60с (скрейп-интервал vmagent 30s + дискавери), затем:
curl -s --get --data-urlencode 'query=demo_queue_depth' http://192.168.71.83:8428/api/v1/query
```

Демо «HPA по CPU» (B1) — не зависит от `metricgen`/Prometheus, можно запускать
сразу после namespace:

```bash
./apply.sh manifests/10-hpa-cpu.yaml
kubectl -n cookbook-k8s rollout status deploy/php-apache --timeout=120s
kubectl -n cookbook-k8s get hpa php-apache   # база: cpu <unknown>/50% → через ~30с 0%/50%
./apply.sh manifests/11-load-gen.yaml
# снимать `kubectl -n cookbook-k8s get hpa php-apache` в цикле — растут реплики
kubectl -n cookbook-k8s delete pod load-gen
# снимать дальше — реплики возвращаются к 1
```

После демо — убрать объекты сразу (быстрее, чем ждать `teardown.sh` в конце
серии; `teardown.sh` тоже подчистит `php-apache`/`load-gen` по имени, если
это не сделать сейчас — см. «Teardown» ниже):

```bash
kubectl -n cookbook-k8s delete -f manifests/10-hpa-cpu.yaml
kubectl -n cookbook-k8s delete pod load-gen --ignore-not-found
```

Демо «HPA по кастомной метрике» (B2) — собственный Prometheus + Adapter +
`consumer`:

```bash
./apply.sh manifests/15-prometheus.yaml
kubectl -n cookbook-k8s rollout status deploy/prometheus --timeout=120s

kubectl -n cookbook-k8s port-forward svc/prometheus 19090:9090 &
kubectl -n cookbook-k8s port-forward svc/metricgen 18080:8080 &
sleep 3
curl -s "http://localhost:18080/push?n=7"
curl -s --get --data-urlencode 'query=demo_queue_depth' http://localhost:19090/api/v1/query
# ожидаемо: непустой result, метки namespace=cookbook-k8s, pod=metricgen-*, service=metricgen

kubectl create namespace custom-metrics
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install prometheus-adapter prometheus-community/prometheus-adapter \
  -n custom-metrics --version 5.3.0 -f manifests/prometheus-adapter-values.yaml
kubectl -n custom-metrics rollout status deploy/prometheus-adapter --timeout=120s

# Git Bash: MSYS_NO_PATHCONV=1 обязателен для kubectl get --raw "/apis/..."
MSYS_NO_PATHCONV=1 kubectl get --raw \
  "/apis/custom.metrics.k8s.io/v1beta1/namespaces/cookbook-k8s/services/metricgen/demo_queue_depth"

./apply.sh manifests/21-hpa-custom.yaml
kubectl -n cookbook-k8s rollout status deploy/consumer --timeout=120s
kubectl -n cookbook-k8s get hpa consumer

curl -s "http://localhost:18080/push?n=40"
# снимать `kubectl -n cookbook-k8s get hpa consumer` каждые 30с — растут реплики к 8
curl -s "http://localhost:18080/push?n=0"
# снимать дальше — реплики возвращаются к 1 (через ~300с, окно стабилизации scaleDown)

kubectl -n cookbook-k8s delete hpa consumer   # HPA снимается после демо, см. выше
```

Демо «KEDA и scale-to-zero» (B3) — установка KEDA + живой переход
`N→0→N→0`:

```bash
kubectl create namespace keda
helm repo add kedacore https://kedacore.github.io/charts
helm repo update kedacore
helm install keda kedacore/keda -n keda --version 2.20.1
kubectl -n keda rollout status deploy/keda-operator --timeout=120s
kubectl -n keda rollout status deploy/keda-operator-metrics-apiserver --timeout=120s
kubectl get crd | grep keda
kubectl get apiservices | grep external.metrics   # v1beta1.external.metrics.k8s.io -> keda/keda-operator-metrics-apiserver, Available

kubectl -n cookbook-k8s port-forward svc/metricgen 18080:8080 &
sleep 3
curl -s "http://localhost:18080/push?n=0"          # опустить метрику ДО apply — обязательный порядок

./apply.sh manifests/30-keda-scaledobject.yaml
# снимать `kubectl -n cookbook-k8s get scaledobject,hpa,deploy consumer` каждые ~15с —
# consumer должен уйти в 0/0 (см. «Живой ряд» выше)

curl -s "http://localhost:18080/push?n=30"
# снимать дальше — consumer поднимается из нуля к ceil(30/5)=6 репликам

curl -s "http://localhost:18080/push?n=0"
# снимать дальше — повторный уход в 0 через ~cooldownPeriod (60с) после деактивации триггера

kubectl -n cookbook-k8s delete scaledobject consumer   # ScaledObject снимается после демо, см. выше
```

Фикстуры снимались через `./capture.sh <name> -- <kubectl-команда>`, команды
приведены дословно в комментариях фикстур (`fixtures/*.txt`, строка `# cmd:`).
Исключение — `fixtures/10-hpa-cpu.txt`: это не разовый `capture.sh`, а живой
ряд из `kubectl -n cookbook-k8s get hpa php-apache` каждые 30с в цикле,
формат и точная последовательность команд — в самом файле и в описании демо
«HPA по CPU» выше (манифесты и порядок запуска демо HPA по CPU — в самом
начале этого раздела, сразу после `metricgen`).

## Изоляция

- Объекты `metricgen` — в общем namespace `cookbook-k8s` (тот же, что и стенд
  `resources`), лейбл `app: metricgen`. Объекты B2 — `app: prometheus`
  (`manifests/15-prometheus.yaml`) и `app: consumer`
  (`manifests/21-hpa-custom.yaml`), тоже в `cookbook-k8s`.
- Prometheus Adapter — отдельный namespace `custom-metrics` (создан явно под
  задачу B2, не общий с `cookbook-k8s`/`resources`). `ScaledObject consumer`
  (`app: consumer`, `manifests/30-keda-scaledobject.yaml`) — в `cookbook-k8s`,
  сам оператор **KEDA** — отдельный namespace `keda` (задача B3, helm-релиз
  `keda`), тоже не общий с `cookbook-k8s`/`resources`/`custom-metrics`.
- Что НЕ трогаем: `kube-system`, `argocd`, `monitoring` (включая конфигурацию
  `vmagent`), `cilium`, `external-secrets`, `sealed-secrets`, `vpa` и любые
  другие системные namespace кластера k8s-volga. Namespace `cookbook-k8s` не
  размечался дополнительными лейблами — не понадобилось (см. «Скрейп»).
  RBAC собственного Prometheus — `Role`/`RoleBinding` только в `cookbook-k8s`
  (НЕ `ClusterRole`), только `get/list/watch` на `pods`.
- `v1beta1.custom.metrics.k8s.io` (Prometheus Adapter, ns `custom-metrics`) и
  `v1beta1.external.metrics.k8s.io` (KEDA, ns `keda`) — два разных API-slot'а
  APIService, регистрируются независимо, конфликта нет: HPA от B2 читал
  `custom.metrics.k8s.io`, `ScaledObject` от B3 — `external.metrics.k8s.io`.
- `resources.requests` подов `metricgen` (100m CPU/128Mi), `prometheus` (100m
  CPU/256Mi) и `consumer` (50m CPU/32Mi × до 8 реплик при полном скейле — до
  400m/256Mi) укладываются в `ResourceQuota cookbook-quota` (4 CPU/4Gi
  requests на namespace) наравне с уже развёрнутыми подами стенда
  `resources`; по факту снятия ряда пиковое использование namespace не
  превышало ~1 CPU / ~600Mi requests из квоты 4 CPU/4Gi.

## Teardown

```bash
bash teardown.sh
```

Удаляет объекты этого стенда из `cookbook-k8s`: `Deployment`/`Service`/
`ConfigMap` с лейблами `app=metricgen`, `app=prometheus` (плюс его
`ServiceAccount`/`Role`/`RoleBinding`), `app=consumer`, любые оставшиеся
`HorizontalPodAutoscaler`/`ScaledObject` этих же меток, а также по имени
(без меток `app=*`) — `php-apache` (`Deployment`/`Service`/`HPA` демо HPA по
CPU) и под `load-gen`, если демо не убрали вручную сразу после снятия ряда
(см. «Порядок запуска» выше). Все команды с `--ignore-not-found` — скрипт
идемпотентен, повторный запуск на уже убранном стенде ничего не ломает.
Namespace **не удаляется** — он общий со стендом `resources`, его
`teardown.sh` сносит целиком отдельно. Операторы (VPA в ns `vpa`, Prometheus
Adapter в ns `custom-metrics`, KEDA в ns `keda`) этот скрипт не трогает — их
жизненный цикл отдельный (Prometheus Adapter снимается вручную: `helm
uninstall prometheus-adapter -n custom-metrics && kubectl delete namespace
custom-metrics`; аналогично KEDA: `helm uninstall keda -n keda && kubectl
delete namespace keda`).

**Состояние после задачи B2** (для B3, для истории): `metricgen`, `consumer`
(Deployment, без HPA — снят после снятия ряда), собственный `prometheus` и
Prometheus Adapter (ns `custom-metrics`) остаются развёрнутыми.
`HorizontalPodAutoscaler consumer` удалён — в B3 на `Deployment consumer`
навешивается `ScaledObject` KEDA (два автоскейлера на одном
`scaleTargetRef` конфликтуют).

**Состояние после задачи B3, проверено живьём при финализации в задаче B4
(`kubectl -n cookbook-k8s get deploy,pod,svc,hpa,scaledobject`).** KEDA 2.20.1
установлена в ns `keda` (helm-релиз `keda`, три `Deployment`, все `Running`) —
**остаётся развёрнутой**. CRD `scaledobjects.keda.sh` и др. — на месте.
APIService `v1beta1.external.metrics.k8s.io` зарегистрирован KEDA и
`Available`. `ScaledObject consumer` **удалён** после снятия ряда (`kubectl
-n cookbook-k8s delete scaledobject consumer`) — вместе с ним автоматически
исчез и созданный KEDA `HorizontalPodAutoscaler keda-hpa-consumer`; ни
`hpa`, ни `scaledobject` в namespace сейчас нет. `Deployment consumer`
остался на `0/0` (последнее состояние от KEDA, не восстановлено вручную до
исходной 1 реплики — не требовалось задачей). `metricgen` (1/1) и
собственный `prometheus` (1/1) — без изменений, развёрнуты и `Running`.
Namespace `custom-metrics`/Prometheus Adapter не тронуты. `teardown.sh` в
этой задаче (B4) **не прогонялся** — состояние выше ещё нужно для проверки
самого скрипта и для сверки README; окончательную уборку
`metricgen`/`prometheus`/`consumer` читатель выполняет через `teardown.sh`,
операторов (Prometheus Adapter, KEDA) — вручную командами выше.

Объекты демо «HPA по CPU» (`php-apache` `Deployment`/`Service`/`HPA`,
`load-gen`) без меток `app=*` — `teardown.sh` снимает их отдельными строками
по имени (не по селектору), см. сам скрипт. Быстрее убрать сразу после
снятия ряда, не дожидаясь `teardown.sh`:

```bash
kubectl -n cookbook-k8s delete -f kubernetes/autoscaling/manifests/10-hpa-cpu.yaml
kubectl -n cookbook-k8s delete pod load-gen --ignore-not-found
```
