# Стенд: ресурсы, requests/limits и QoS (k8s-volga)

Живые замеры для статьи серии «Kubernetes на практике»: QoS-классы, throttling,
OOMKill, ограждения namespace (ResourceQuota/LimitRange), рекомендации VPA.

## Требования к кластеру

- Kubernetes 1.36+.
- `metrics-server` — обязателен: без него не работает ни `kubectl top`, ни VPA-рекомендации
  (`vpa-recommender` берёт исторические данные через Metrics API).
- kube-prometheus-stack (для метрик throttling/OOMKill в Grafana, опционально — для демо
  достаточно `kubectl exec`/`kubectl top`/`kubectl describe`).
- Права на создание namespace и CRD (`verticalpodautoscalers`,
  `verticalpodautoscalercheckpoints`) — нужны для namespace `cookbook-k8s`,
  `cookbook-k8s-noqos` и установки VPA.
- Кластер на Talos Linux: к узлам нет SSH и нет доступа к ноде напрямую, всё наблюдение —
  через `kubectl exec`/`kubectl top`/`kubectl describe`, ничего мимо API-сервера. Cgroup —
  v2 (`/sys/fs/cgroup/cpu.stat` внутри контейнера читается напрямую, без пересчёта из v1).

## Таблица версий

| Компонент | Версия |
|---|---|
| Kubernetes | 1.36.2 |
| pause | `registry.k8s.io/pause:3.10` |
| polinux/stress | `polinux/stress:1.0.4` |
| VPA (vertical-pod-autoscaler) | `vertical-pod-autoscaler-1.7.0` |

## Изоляция и безопасность

- Все демо разворачиваются в namespace `cookbook-k8s` (создаётся `manifests/00-namespace.yaml`).
- `ResourceQuota cookbook-quota` — потолок namespace: `requests.cpu 4`, `requests.memory 4Gi`,
  `limits.cpu 8`, `limits.memory 8Gi`, `pods 30`.
- `LimitRange cookbook-limits` — дефолты и потолок на контейнер: request `100m`/`64Mi`,
  limit по умолчанию `200m`/`128Mi`, максимум `2`/`2Gi`.
- Что НЕ трогаем: `kube-system`, `argocd`, `monitoring`, `cilium`, `external-secrets`,
  `sealed-secrets` и любые другие системные namespace кластера k8s-volga.
- VPA-оператор развёрнут в выделенный namespace `vpa` (не `kube-system`): CRD
  (`verticalpodautoscalers`, `verticalpodautoscalercheckpoints`) + RBAC + `vpa-recommender` +
  `vpa-updater` из официальных манифестов `kubernetes/autoscaler` тега
  `vertical-pod-autoscaler-1.7.0` (`deploy/vpa-v1-crd-gen.yaml`, `deploy/vpa-rbac.yaml`,
  `deploy/recommender-deployment.yaml`, `deploy/updater-deployment.yaml`), с заменой
  `namespace: kube-system` → `namespace: vpa` (манифесты релиза хардкодят `kube-system`,
  штатный `hack/vpa-up.sh` не параметризует namespace). `admission-controller` не разворачивался
  (требует отдельной генерации TLS-сертификатов вебхука, тоже завязанной на `kube-system`) —
  для демо `updateMode: Off` он не нужен: рекомендации считает только `vpa-recommender`, который
  Running. `vpa-updater` поднят и Running, но в режиме `Off` бездействует (не применяет
  рекомендации, не пересоздаёт поды). Namespace `vpa` и оба деплоя оставлены установленными.

## Демо

- **QoS-классы** (`manifests/10-qos-guaranteed.yaml`, `11-qos-burstable.yaml`, `12-qos-besteffort.yaml`,
  `fixtures/10-qos.txt`) — три пода с разными requests/limits показывают все три класса QoS живьём:
  `guaranteed` → `Guaranteed` (requests == limits по CPU и памяти), `burstable` → `Burstable`
  (requests < limits). Ключевой результат: `LimitRange cookbook-limits` инжектит дефолтные
  requests/limits в поды без ресурсов, поэтому чистый `BestEffort` в `cookbook-k8s` получить
  нельзя — под без ресурсов там всё равно получает дефолты и становится `Burstable`. Честный
  `BestEffort` показан в отдельном временном namespace `cookbook-k8s-noqos` (без LimitRange).
- **CPU throttling / CFS-квоты** (`manifests/20-cpu-throttle.yaml`, `fixtures/20-cpu-throttle.txt`) —
  под `stress --cpu 4` (4 потока хотят по ядру каждый) с `limits.cpu 500m` показывает живьём,
  как ядро режет контейнер через CFS-квоты cgroup v2 в 100ms-окне: `cpu.stat` внутри контейнера
  (`/sys/fs/cgroup/cpu.stat`) — `nr_periods == nr_throttled` (под троттлится в каждом 100ms-окне,
  доля троттлинга = 1.0), между двумя снимками `nr_throttled` вырос с `853` до `1137` (+284),
  `throttled_usec` — с `142285008` до `189639688` мкс. Интервал между снимками считается по самим
  окнам: 284 периода × 100ms = 28.4s; `usage_usec` за них вырос на 14235772 мкс = 14.24s CPU-времени,
  т.е. 0.501 ядра. Это сходится с `kubectl top`: `CPU 502m` — ровно на уровне `limit`, хотя четыре
  потока внутри хотят ~4000m, в восемь раз больше квоты. Урок: контейнер «тормозит» даже когда
  среднее потребление CPU выглядит невысоким — упирается не в среднее, а в квоту каждого 100ms-окна;
  и `kubectl top` в такой ситуации показывает не потребность, а потолок.
- **OOMKill** (`manifests/30-oomkill.yaml`, `fixtures/30-oomkill.txt`) — под `stress --vm-bytes 250M`
  с `limits.memory 128Mi` показывает живьём: память не «подождёт», как CPU. При упоре в
  `memory.max` ядро сперва пытается вернуть память (страничный кеш, чистые файловые страницы),
  притормаживая аллокации, но здесь запрошена анонимная память и swap'а нет — возвращать нечего,
  и контейнер убивает cgroup OOM killer. Живые значения: `reason=OOMKilled exitCode=137
  restartCount=3`, события `BackOff` — под уходит в CrashLoopBackOff и рестартует снова и снова
  по мере роста `restartCount`.
- **Ограждения namespace: LimitRange-дефолты и отказ ResourceQuota**
  (`manifests/41-limitrange-default.yaml`, `manifests/40-quota-reject.yaml`, `fixtures/40-guardrails.txt`) —
  под `no-resources` без единого поля `resources` живьём получает от `LimitRange cookbook-limits`
  дефолты `requests {cpu: 100m, memory: 64Mi}` и `limits {cpu: 200m, memory: 128Mi}`. Следом
  `Deployment quota-buster` (6 реплик по `requests.memory 1Gi`) упирается в `ResourceQuota
  cookbook-quota` (потолок `requests.memory 4Gi`, из него уже заняты 192Mi — по 64Mi
  подами `guaranteed`, `burstable` и `no-resources`, последнему их проставил LimitRange; отсюда
  `used: requests.memory=3264Mi` = 3×1Gi поднявшихся реплик + 192Mi):
  реально поднялись только `3/6` реплик, остальные — `FailedCreate` с точным текстом `pods
  "quota-buster-…" is forbidden: exceeded quota: cookbook-quota, requested: requests.memory=1Gi,
  used: requests.memory=3264Mi, limited: requests.memory=4Gi`. Урок: namespace защищает себя даже
  без вмешательства человека — LimitRange не даёт создать под без ресурсов, а ResourceQuota режет
  масштабирование на суммарном лимите, а не на лимите одного пода.
- **VPA-рекомендации, `updateMode: Off`** (`manifests/50-vpa-sample.yaml`,
  `fixtures/50-vpa-recommendation.txt`) — под `stress --cpu 1 --vm-bytes 80M` с заведомо
  заниженными `requests {cpu: 50m, memory: 32Mi}` (при `limits {cpu: 500m, memory: 256Mi}`) под
  наблюдением `VerticalPodAutoscaler` в
  режиме `Off`: оператор считает рекомендации, но НЕ пересоздаёт под и не меняет его requests
  (в отличие от `Auto`/`Recreate`). Через ~5 минут накопления истории `vpa-recommender` по
  наблюдаемому потреблению (`kubectl top`: `501m` CPU / `80Mi` память) живьём выдал
  `target={cpu: 587m, memory: 250Mi}` — почти в 12 раз выше заданного `request.cpu` (по памяти
  соотношение обманчиво, см. ниже про пол рекомендации). Урок: `Off` — безопасный способ получить honest-числа для
  requests/limits на проде без риска рестарта подов; `Auto`/`Recreate` пересоздают под под новые
  requests и годятся не для всякой нагрузки.
  ⚠️ **`501m` в `kubectl top` — это НЕ фактическая потребность, а упор в собственный лимит пода.**
  `stress --cpu 1` хочет ~1000m, а `limits.cpu` у `vpa-sample` — `500m`: тот же троттлинг по
  CFS-квоте, что и в демо `20-cpu-throttle`, только на одном потоке. Recommender считает по тому,
  что видит через Metrics API, а видит он обрезанное лимитом наблюдение — поэтому `target.cpu 587m`
  нельзя трактовать как «переоценку ×1.17 относительно факта»: относительно реальной потребности
  (~1000m) это скорее НЕдооценка. Общий вывод сильнее частного: VPA рекомендует по тому, что
  наблюдает, а наблюдение ограничено сверху уже поставленным лимитом — занижен лимит, и
  рекомендация аккуратно этот заниженный лимит оправдает.
  ⚠️ **Уверенность в этой конкретной рекомендации низкая** — окно наблюдения короткое (~5 минут,
  минимум для VPA обычно сутки+). ⚠️ **`250Mi` по памяти — это пол рекомендации, а не переоценка
  по короткому окну.** Лимит наблюдение по памяти не режет (`256Mi` против потребления ~80Mi), но
  в фикстуре `target.memory` и `lowerBound.memory` совпадают В ТОЧНОСТИ (`250Mi` в обоих), тогда
  как по CPU те же две оценки разошлись (`587m` против `307m`). Совпадение двух разных оценок на
  одном значении — признак упора в нижнюю границу, а не разброса: `250Mi` — ровно дефолтный пол
  vpa-recommender (`--pod-recommendation-min-memory-mb=250`, т.е. 250 × 1024 × 1024 Б). Отсюда
  два следствия: (1) длина окна тут ни при чём — при тех же ~80Mi recommender вернёт те же `250Mi`
  и через неделю истории; (2) «`target.memory` в 8 раз выше `request.memory`, значит VPA опознал
  занижение» по памяти ничего не доказывает — это артефакт пола (requests действительно занижены,
  но показывает это CPU-число, а не память). Урок: увидев ровное значение пола, не принимай его за
  измеренную потребность. `upperBound` в фикстуре
  (`cpu: 224997m`, `memory: 42092091239` байт ≈ 39Gi) — не опечатка и не рекомендация к
  применению, а артефакт низкой уверенности recommender'а на коротком окне: чем меньше история,
  тем шире доверительный интервал и тем бесполезнее верхняя граница. На проде с VPA `Off` нужно
  ждать накопления полноценной истории (дни, не минуты), прежде чем доверять `target`, и
  `upperBound` игнорировать вовсе, пока окно не устоится.

## Порядок запуска

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl config current-context   # должен быть admin@k8s-volga

# namespace + ResourceQuota + LimitRange
./apply.sh manifests/00-namespace.yaml

# QoS-классы (Guaranteed/Burstable в cookbook-k8s, BestEffort — в отдельном namespace)
./apply.sh manifests/10-qos-guaranteed.yaml
./apply.sh manifests/11-qos-burstable.yaml
./apply.sh manifests/12-qos-besteffort.yaml   # создаёт ns cookbook-k8s-noqos

# CPU throttling — снять cpu.stat сразу и ещё раз через ~30с (в фикстуре интервал 28.4с,
# посчитанный по nr_periods; см. «Демо»)
./apply.sh manifests/20-cpu-throttle.yaml

# OOMKill — подождать несколько рестартов (restartCount растёт)
./apply.sh manifests/30-oomkill.yaml

# ограждения namespace: сначала LimitRange-дефолт, потом отказ по квоте
./apply.sh manifests/41-limitrange-default.yaml
./apply.sh manifests/40-quota-reject.yaml

# VPA-рекомендации — сначала установить оператор (см. «Установка VPA» ниже),
# затем применить демо и подождать ~5 минут накопления истории у recommender'а
./apply.sh manifests/50-vpa-sample.yaml
```

Манифесты пронумерованы по порядку применения. Живые выводы для фикстур снимались через
`./capture.sh <name> -- <kubectl-команда>` (пример — в `capture.sh`), команды приведены
дословно в комментариях самих фикстур (`fixtures/*.txt`, строка `# cmd:`).

После снятия нужных фикстур «жгущие» демо-объекты (`cpu-throttle`, `oomkill`, `quota-buster`,
`vpa-sample`) удаляются вручную (`kubectl delete`), чтобы не расходовать ресурсы кластера между
задачами; лёгкие `pause`-поды (`guaranteed`, `burstable`, `besteffort`) можно оставлять —
полный сброс делает `teardown.sh`.

## Установка VPA

Официальный `hack/vpa-up.sh` из тега `vertical-pod-autoscaler-1.7.0` хардкодит
`namespace: kube-system` во всех манифестах релиза и не параметризует его — поэтому оператор
ставится вручную, применением манифестов из `deploy/` с заменой namespace на `vpa`:

```bash
git clone --branch vertical-pod-autoscaler-1.7.0 --depth 1 \
  https://github.com/kubernetes/autoscaler.git
cd autoscaler/vertical-pod-autoscaler

kubectl create namespace vpa

kubectl apply -f deploy/vpa-v1-crd-gen.yaml   # CRD, cluster-scoped, без изменений namespace
sed 's/namespace: kube-system/namespace: vpa/' deploy/vpa-rbac.yaml | kubectl apply -f -
sed 's/namespace: kube-system/namespace: vpa/' deploy/recommender-deployment.yaml | kubectl apply -f -
sed 's/namespace: kube-system/namespace: vpa/' deploy/updater-deployment.yaml | kubectl apply -f -
```

Замена безопасна: все объекты внутри каждого файла (Deployment, ServiceAccount,
Role/RoleBinding и их subjects) ссылаются на один и тот же namespace, `sed` меняет их
консистентно.

`admission-controller` этим способом **не разворачивается** — он требует отдельной генерации
TLS-сертификатов вебхука (`pkg/admission-controller/gencerts.sh`), тоже завязанной на
предположение о `kube-system`. Для демо с `updateMode: Off` он не нужен: рекомендации считает
только `vpa-recommender`, `vpa-updater` в `Off` не применяет их и не пересоздаёт поды. Если
понадобится `updateMode: Auto`/`Recreate` (реальное применение рекомендаций к подам) —
`admission-controller` придётся ставить и сертификаты вебхука готовить отдельно для namespace `vpa`.

Проверка, что оператор поднялся:

```bash
kubectl -n vpa get deploy
# vpa-recommender   1/1   ...
# vpa-updater       1/1   ...
```

## Teardown

⚠️ `teardown.sh` удаляет **общий** namespace `cookbook-k8s` целиком — вместе с объектами
соседних стендов серии (autoscaling, stateful, secrets, operators, architecture живут в нём же).
Запускайте, только если сносите всю серию. Поэтому скрипт требует явного подтверждения — без
переменной `CONFIRM_DELETE_NAMESPACE=cookbook-k8s` он завершается отказом, ничего не удаляя:

```bash
CONFIRM_DELETE_NAMESPACE=cookbook-k8s bash teardown.sh
```

Удаляет namespace `cookbook-k8s` и `cookbook-k8s-noqos` целиком (все демо-объекты внутри —
поды, Deployment'ы, ResourceQuota, LimitRange, VPA-объект `vpa-sample`). Namespace `vpa` (сам
VPA-оператор — `vpa-recommender`/`vpa-updater`) `teardown.sh` **не трогает**: он общий для
кластера и не относится к жизненному циклу этого конкретного стенда — удалять его отдельно,
если он больше не нужен ни одному демо на кластере.
