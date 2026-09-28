# Стенд: архитектура Kubernetes — control plane и путь применения (k8s-volga)

Живые замеры для серии «Kubernetes на практике»: из чего реально состоит control plane
кластера, кто из его компонентов сейчас ведущий, один контринтуитивный факт про etcd — и что
на самом деле происходит между одним `kubectl apply` и запущенным контейнером.

## Назначение

Показать control plane не как схему из документации, а как то, что реально работает в
конкретном кластере: сколько экземпляров каждого компонента, кто из них сейчас активен и как
это подтверждается живьём (через аренду `Lease`), и почему привычное «etcd — статик-под в
`kube-system`» здесь не выполняется.

Второе демо достраивает путь применения манифеста: из одного `kubectl apply` рождается цепочка
объектов, и события показывают, кто именно создал каждый шаг — четыре разных источника, ни один
из которых не знает про остальных.

Третье демо ломает этот путь в трёх разных местах и показывает, что `Pending`,
`ImagePullBackOff` и `CrashLoopBackOff` — не три разные болезни, а три разные точки остановки
на одном и том же пути.

## Требования к кластеру

- Kubernetes 1.36+.
- Доступ **на чтение** к `kube-system` и к узлам (`get`/`list` подов, `Lease`, узлов) — там
  стенд только читает, ничего не создаёт и не меняет.
- В namespace `cookbook-k8s` — права **`create`/`delete`** на Deployment и Pod и **чтение**
  `Event`: демо 1 создаёт свой Deployment (поды создаёт уже контроллер) и снимает по нему
  события, демо 2 создаёт три голых Pod и удаляет их сразу после снятия фикстуры, а
  `teardown.sh` удаляет созданное по метке `stand: cookbook-architecture`.
- **Кластер с несколькими control-plane узлами** — чтобы было видно HA: несколько экземпляров
  `kube-apiserver`/`kube-scheduler`/`kube-controller-manager` и лидерство через аренду. На
  одиночном control plane демо 0 вырождается: `Lease` есть, но держатель у неё всегда
  единственно возможный, и «аренду продлевает тот же держатель» перестаёт быть наблюдением.
- В namespace `cookbook-k8s` действуют `LimitRange` (на k8s-volga — `cookbook-limits`,
  `max` `cpu: 2` **на контейнер**) и `ResourceQuota` (`requests.cpu` `4` на namespace):
  демо 2 упирается в них и разбирает это как отдельный факт. На кластере без квоты на
  `requests.cpu` под `stuck-pending` можно сделать честнее — через `requests.cpu` больше
  `allocatable` любого узла; см. комментарий в `manifests/20-stuck-pending.yaml`.
- Namespace `cookbook-k8s` уже существует (создан стендом `../resources`, общий для всех
  стендов серии) — этот стенд свой namespace не создаёт.

## Таблица версий

Все значения — из `fixtures/00-control-plane.txt` (образы — из фикстур демо 1 и 2, где они
видны в тексте событий kubelet).

| Компонент | Версия | Откуда |
|---|---|---|
| Kubernetes (сервер) | `v1.36.2` | `00-control-plane.txt`, `Server Version` |
| Kubernetes (клиент `kubectl`) | `v1.36.1` | `00-control-plane.txt`, `Client Version` |
| Kustomize (в составе `kubectl`) | `v5.8.1` | `00-control-plane.txt`, `Kustomize Version` |
| kubelet на всех шести узлах | `v1.36.2` | `00-control-plane.txt`, колонка `VERSION` |
| ОС узлов | Talos `v1.13.6` | `00-control-plane.txt`, колонка `OS` |
| Среда исполнения | `containerd://2.2.5` | `00-control-plane.txt`, колонка `RUNTIME` |
| Образ демо 1 и `stuck-pending` | `registry.k8s.io/pause:3.10` | `10-apply-path.txt`, событие `Pulled` |
| Образ `stuck-crashloop` | `busybox:1.37` | `20-stuck.txt`, событие `Pulled` |
| Несуществующий реестр демо 2 | `example.invalid/nope:0.0.1` | `20-stuck.txt`, событие `Failed` |

Клиент на версию младше сервера (`v1.36.1` против `v1.36.2`) — штатная ситуация: разница в
патч-версии на вывод демо не влияет.

## Изоляция и безопасность

- **Control plane — только чтение.** Против `kube-system`, статик-подов, конфигурации Talos и
  самих узлов стенд выполняет только `kubectl get`/`api-versions`/`version` — ни одной команды
  `create`/`patch`/`delete`/`cordon`/`drain`. Свои тейнты стенд не ставит, узлы не дренирует,
  метки узлов не меняет. Записывает стенд ровно в одно место — namespace `cookbook-k8s`, свои
  собственные объекты (см. следующий пункт).
- Демо-объекты — только в существующем namespace `cookbook-k8s`, все с меткой
  `stand: cookbook-architecture`. Это Deployment `trace-demo`
  (`manifests/10-trace-target.yaml`): 2 реплики `registry.k8s.io/pause:3.10`, requests
  `50m`/`32Mi`, limits `100m`/`64Mi` на контейнер — и три коротко живущих пода демо 2
  (`stuck-pending`, `stuck-imagepull`, `stuck-crashloop`, requests `10m`/`16Mi`, limits
  `50m`/`32Mi` каждый). Поды демо 2 существуют только на время снятия фикстуры и удаляются
  сразу после: залипший под держит квоту namespace, ничего не отдавая взамен.
- Что не трогаем: `kube-system` целиком (статик-поды `kube-apiserver`/`kube-scheduler`/
  `kube-controller-manager`, аренды `Lease`, любые системные объекты), конфигурацию узлов и
  Talos, любые другие системные namespace кластера k8s-volga (`argocd`, `monitoring`,
  `cilium`, `external-secrets`, `sealed-secrets`, `vpa` и т. п.), стенды фаз 1–3
  (`resources`, `autoscaling`, `stateful`, `secrets`, `networking`, `operators`) в общем
  namespace `cookbook-k8s`.
- Демо 2 намеренно обращается только к домену `example.invalid` — зона `.invalid`
  зарезервирована RFC 2606 и не может существовать: ни один настоящий реестр стенд не дёргает
  и ни в чей rate-limit не упирается.
- `teardown.sh` удаляет только объекты этого стенда по метке `stand: cookbook-architecture` в
  `cookbook-k8s` — namespace остаётся, в `kube-system` скрипт не заходит вовсе.
- **Предупреждения PodSecurity при `apply` — ожидаемы, это не сбой.** Namespace
  `cookbook-k8s` помечен для PodSecurity Admission в режиме `warn` (не `enforce`), а манифесты
  стенда намеренно не задают `securityContext`, поэтому контроллер печатает предупреждение о
  несоответствии профилю (`allowPrivilegeEscalation`, `capabilities`, `runAsNonRoot`,
  `seccompProfile` — конкретный набор зависит от уровня, выставленного на namespace). В режиме
  `warn` объекты всё равно создаются: живой прогон с этими предупреждениями дал 2/2 Running у
  `trace-demo` и все три ожидаемых состояния в демо 2. `securityContext` здесь не выставлен сознательно:
  предмет стенда — путь применения и стадии залипания пода, а не настройка профиля
  безопасности; добавленные поля дали бы в выводе `kubectl` шум, к разбираемому механизму
  отношения не имеющий.

## Демо 0: control plane

Фикстура: `fixtures/00-control-plane.txt`. Мутаций нет — весь блок снимается чтением.

- Шесть узлов: три control-plane (`k8s-volga-cp01/02/03`), у каждого тейнт
  `node-role.kubernetes.io/control-plane:NoSchedule`, и три worker-узла без тейнтов.
- По три экземпляра `kube-apiserver`, `kube-controller-manager`, `kube-scheduler` — по одному
  на каждый control-plane узел, все `Running`.
- **Подов etcd в кластере нет вовсе** — поиск по всем namespace ничего не находит. На Talos
  etcd работает вне Kubernetes (отдельным процессом ОС, не под управлением kubelet), поэтому
  типовое представление «etcd — статик-под в `kube-system`», верное для kubeadm, здесь не
  выполняется.
- Лидерство `kube-scheduler` и `kube-controller-manager` держится арендой (`Lease` в
  `kube-system`, имена ровно `kube-scheduler`/`kube-controller-manager`): два снимка
  `renewTime` показывают один и тот же `holderIdentity` (`k8s-volga-cp01_...`) с
  продвинувшимся временем продления — аренду продлевает тот же держатель, смены лидера не
  было. Между снимками в команде стоит `sleep 20`, но фактический разрыв по меткам времени
  больше: `07:55:03.810125Z` → `07:55:35.992875Z` у `kube-controller-manager` и
  `07:55:03.373783Z` → `07:55:35.522676Z` у `kube-scheduler`, то есть **≈32–33 секунды** —
  `sleep` плюс накладные расходы на сами вызовы `kubectl`. Заголовок блока в фикстуре
  («те же аренды через 20 с») называет параметр команды, а не измеренный интервал; измеренный
  считается по меткам.
- API server отдаёт 44 API-группы/версии (`kubectl api-versions`) — единственная точка входа,
  через которую проходят все запросы: и `kubectl`, и сами компоненты control plane.

## Демо 1: путь применения

Фикстура: `fixtures/10-apply-path.txt`, снято живьём на k8s-volga.

### Что отправляет kubectl

`kubectl apply -v=6` показывает, что клиент делает не «один запрос на создание», а серию.
До записи объекта уходят только чтения — схема (`/openapi/v3`, `/openapi/v3/apis/apps/v1`),
список групп (`/api`, `/apis`), проверка, что объекта ещё нет
(`GET /apis/apps/v1/namespaces/cookbook-k8s/deployments/trace-demo` → `404 Not Found`) и
проверка namespace (`GET /api/v1/namespaces/cookbook-k8s` → `200 OK`). Запись — ровно одна:

```
POST .../apis/apps/v1/namespaces/cookbook-k8s/deployments?fieldManager=kubectl-client-side-apply&fieldValidation=Strict  201 Created
```

Дальше kubectl не участвует вообще: он создал ОДИН объект — Deployment. Ни ReplicaSet, ни
подов он не создаёт и про них не знает.

### Что получилось из одного объекта

```
deployment.apps/trace-demo             2/2   2   2
replicaset.apps/trace-demo-58b97d487c  2     2   2
pod/trace-demo-58b97d487c-4lj98        1/1   Running
pod/trace-demo-58b97d487c-zczbk        1/1   Running
```

Четыре объекта вместо одного. Связь между ними — не по именам, а по `ownerReferences`:

```
pod=trace-demo-58b97d487c-4lj98 owner=ReplicaSet/trace-demo-58b97d487c
rs=trace-demo-58b97d487c        owner=Deployment/trace-demo
```

### Кто создал каждый шаг

События с реальными источниками (порядок появления событий восстановлен по наносекундному
суффиксу в имени события — в колонке времени разрешение всего одна секунда, и первые шесть
событий уложились в неё). Наносекунда в имени — это момент **записи** события, а не момент
самого действия:

| Объект | Reason | Кто источник |
|---|---|---|
| `Deployment/trace-demo` | `ScalingReplicaSet` (`Scaled up replica set trace-demo-58b97d487c from 0 to 2`) | `deployment-controller` |
| `ReplicaSet/trace-demo-58b97d487c` | `SuccessfulCreate` ×2 (`Created pod: …-4lj98`, `Created pod: …-zczbk`) | `replicaset-controller` |
| `Pod/…-4lj98`, `Pod/…-zczbk` | `Scheduled` | `default-scheduler` |
| `Pod/…-4lj98`, `Pod/…-zczbk` | `Pulled` → `Created` → `Started` | `kubelet` |

Четыре разных источника на цепочку из одного `apply` — и ни один из них не знает про
остальных: каждый видит только свой объект в API и приводит его к желаемому состоянию.

**Оговорка про часы — где наносекунды сравнивать можно, а где нельзя.** Первые пять событий
в наносекундном списке (`ScalingReplicaSet`, `SuccessfulCreate` ×2, `Scheduled` ×2) пишет один
и тот же хост: и `kube-controller-manager`, и `kube-scheduler` держат аренду на
`k8s-volga-cp01` (обе аренды видны в `fixtures/00-control-plane.txt`, там же
`reportingInstance` планировщика — `default-scheduler-k8s-volga-cp01`). Часы одни, поэтому
сравнение наносекунд корректно и порядок этих пяти событий доказан фикстурой. Здесь нужна
ещё одна оговорка: снимок аренд сделан в `07:55:36Z`, то есть за полчаса до самих событий
(`08:27:57Z`), и напрямую на момент события доказан только хост планировщика — через
`reportingInstance`. Держатель аренды `kube-controller-manager` на этот момент не снят;
признаков смены лидера нет (в обеих выборках аренды — один и тот же
`holderIdentity` с тем же UUID, `renewTime` продвигается штатно). А события
`Pulled`/`Created`/`Started` пишут kubelet'ы **разных** узлов — `k8s-volga-wk03` и
`k8s-volga-wk02` (поле `.source.host`, блок «узел-источник каждого события» в фикстуре).
Внутри одного пода эти события с одних часов, но их **взаимный** порядок между двумя подами
опирается на синхронность часов двух узлов и фикстурой не доказан.

Событие `Pulled` здесь гласит
`Container image "registry.k8s.io/pause:3.10" already present on machine and can be accessed
by the pod` — образ уже был на узлах, скачивания не потребовалось. Reason всё равно `Pulled`:
он означает «образ готов», а не «образ скачан».

### Кто принял решение о размещении (стык со статьёй 9)

```
trace-demo-58b97d487c-4lj98   <none>   default-scheduler   default-scheduler-k8s-volga-cp01   Successfully assigned cookbook-k8s/trace-demo-58b97d487c-4lj98 to k8s-volga-wk03
trace-demo-58b97d487c-zczbk   <none>   default-scheduler   default-scheduler-k8s-volga-cp01   Successfully assigned cookbook-k8s/trace-demo-58b97d487c-zczbk to k8s-volga-wk02
```

Обе реплики сели на разные worker-узлы: `k8s-volga-wk03` и `k8s-volga-wk02`. Решение принял
`default-scheduler`, причём конкретный экземпляр — `default-scheduler-k8s-volga-cp01`, то есть
тот самый держатель аренды `kube-scheduler`, который виден в `fixtures/00-control-plane.txt`.

Отдельная деталь фикстуры: у события `Scheduled` поле `.source.component` ПУСТОЕ
(`<none>`) — планировщик пишет события через `events.k8s.io/v1`, где источник лежит в
`.reportingComponent`/`.reportingInstance`, а не в устаревшем `.source`. Контроллеры и kubelet
здесь ещё пишут по-старому, поэтому у них `.source.component` заполнен. Фильтр событий только
по `.source.component` планировщик просто не увидит.

## Демо 2: три места залипания

Фикстура: `fixtures/20-stuck.txt`, снято живьём на k8s-volga.

Три самых частых «зависших» состояния пода — это не три разные болезни, а три разные **точки
остановки на одном и том же пути** из демо 1. Поэтому диагностика начинается с одного вопроса:
до какой стадии под успел дойти.

### Что видно в одной таблице

```
NAME              PHASE     REASON             RESTARTS   NODE
stuck-pending     Pending   <none>             <none>     <none>
stuck-imagepull   Pending   ImagePullBackOff   0          k8s-volga-wk03
stuck-crashloop   Running   CrashLoopBackOff   4          k8s-volga-wk03
```

Первое, что здесь стоит заметить: **`PHASE` сама по себе ничего не говорит о стадии.**
У `stuck-pending` и `stuck-imagepull` фаза одна и та же — `Pending`, хотя остановились они в
разных местах: у первого узла нет вообще, у второго узел уже назначен. А `stuck-crashloop`
показывает `Running` — при `restartCount` 4 и `CrashLoopBackOff` в причине ожидания.

### Стадию показывает не фаза, а `PodScheduled` и `nodeName`

```
NAME              SCHEDULED   NODE             READY
stuck-pending     False       <none>           <none>
stuck-imagepull   True        k8s-volga-wk03   False
stuck-crashloop   True        k8s-volga-wk03   False
```

Это и есть разделение стадий: `PodScheduled=False` и пустой `nodeName` — планировщик своё
решение ещё не принял, под до узла не доехал. `PodScheduled=True` с назначенным узлом —
планировщик отработал, дальше всё происходит уже на узле, и виноват в остановке kubelet, а не
он. У `stuck-pending` условия `Ready` нет вовсе (`<none>`) — оно появляется только после
назначения на узел.

### Три состояния — три стадии

| Состояние | Стадия пути | Кто зафиксировал | Что смотреть |
|---|---|---|---|
| `Pending`, узла нет | отбор узла (планировщик) | `default-scheduler`, событие `FailedScheduling` | разбор предикатов в событии: сколько узлов отпало и почему |
| `ImagePullBackOff` / `ErrImagePull` | получение образа на узле | `kubelet`, события `Pulling` → `Failed` | текст ошибки реестра в событии `Failed` |
| `CrashLoopBackOff` | запуск контейнера | `kubelet`, события `Started` → `BackOff` | `restartCount` и `lastState.terminated` (код выхода), затем логи контейнера |

### Стадия 1: планировщик не нашёл узла

```
Warning  FailedScheduling  2m54s  default-scheduler  0/6 nodes are available: 3 node(s) didn't match Pod's node affinity/selector, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 6 Preemption is not helpful for scheduling.
```

Событие — не «не получилось», а **отчёт по каждому узлу**: из шести узлов три отпали по
несовпадению меток (`nodeSelector` пода требует несуществующей метки), три — по тейнту, который
под не терпит; это control-plane узлы с
`node-role.kubernetes.io/control-plane:NoSchedule` из демо 0. Читать это как «три узла селектор
прошли» нельзя: требуемой метки нет ни на одном из шести узлов, просто планировщик относит
каждый узел к одной причине и складывает узлы в группы, а не перечисляет все провалившиеся
предикаты по каждому. Вторая половина строки —
про вытеснение: планировщик проверил, не поможет ли выселить кого-то менее приоритетного, и
получил «не поможет» для всех шести узлов.

**Почему `stuck-pending` сделан через `nodeSelector`, а не через огромный `requests.cpu`.**
Замысел был проще — попросить больше CPU, чем есть у самого крупного узла (`3950m` на воркерах
k8s-volga), и получить `Insufficient cpu` в разборе предикатов. На этом кластере так нельзя, и
поперёк встают два разных правила. Первым — `LimitRange cookbook-limits` с `max` `cpu: 2`,
`memory: 2Gi` на контейнер: запрос на 5 CPU объектом не становится вовсе:

```
Error from server (Forbidden): error when creating "STDIN": pods "stuck-pending-cpu" is forbidden: maximum cpu usage per Container is 2, but limit is 5
```

Это **отказ на admission**, ещё до записи в etcd, — принципиально другая стадия, чем отказ
планировщика: объекта нет, `kubectl get pod stuck-pending-cpu` отвечает `NotFound`, событий
тоже нет, потому что событиям не о чем сообщать. Смотреть в такой ситуации нужно не
`describe pod`, а текст ответа `kubectl apply`.

Но `LimitRange` тут — только первое препятствие, и потолок пода он не задаёт: `max` в нём
указан **на контейнер**, о чём прямо говорит и сам текст отказа (`maximum cpu usage per
Container is 2`). Под из двух контейнеров по `1525m` прошёл бы `LimitRange` без вопросов.
Настоящий потолок задаёт `ResourceQuota` namespace: `requests.cpu` `4`, из них занято `950m`,
остаток — `3050m`. А свободного CPU на самом загруженном воркере (`wk02`: занято `830m` из
`3950m`) — `3120m`; на `wk01` `3170m`, на `wk03` `3320m`. Меньше `3120m` свободного нет ни на
одном воркере, а больше `3050m` квота не пропустит — то есть **любой под, который прошёл квоту,
помещается на любой воркер**. Запас всего `70m`, но знак от этого не меняется.

Отсюда и вывод: `Insufficient cpu` — самая частая причина `Pending` в проде — на этом кластере
недостижима, и закрывает её квота namespace, а не `LimitRange`. Между «квота разрешает» и
«узел вмещает» просто нет зазора. Поэтому выбрана та же стадия (планировщик), но другой
предикат — заведомо невыполнимый `nodeSelector: khorost.tech/nonexistent-hardware: "true"`,
requests при этом намеренно скромные (`10m`/`16Mi`), чтобы ресурсы в отборе вообще не
участвовали.

### Стадия 2: узел выбран, образа нет

```
Normal   Scheduled  2m54s                default-scheduler  Successfully assigned cookbook-k8s/stuck-imagepull to k8s-volga-wk03
Normal   Pulling    79s (x4 over 2m53s)  kubelet            spec.containers{nope}: Pulling image "example.invalid/nope:0.0.1"
Warning  Failed     79s (x4 over 2m53s)  kubelet            spec.containers{nope}: Failed to pull image "example.invalid/nope:0.0.1": failed to pull and unpack image "example.invalid/nope:0.0.1": failed to resolve reference "example.invalid/nope:0.0.1": failed to do request: Head "https://example.invalid/v2/nope/manifests/0.0.1": Service Unavailable
Warning  Failed     79s (x4 over 2m53s)  kubelet            spec.containers{nope}: Error: ErrImagePull
Normal   BackOff    0s (x11 over 2m53s)  kubelet            spec.containers{nope}: Back-off pulling image "example.invalid/nope:0.0.1"
Warning  Failed     0s (x11 over 2m53s)  kubelet            spec.containers{nope}: Error: ImagePullBackOff
```

Первая же строка снимает подозрение с планировщика: `Successfully assigned … to k8s-volga-wk03`,
своё дело он сделал. Дальше говорит только `kubelet`, и он называет конкретный шаг, на котором
споткнулся, — HTTP-запрос `HEAD https://example.invalid/v2/nope/manifests/0.0.1` к реестру.
Полезно, что в тексте видно и путь Registry API, и ответ, — по нему сразу отличают
недоступность реестра от «нет такого тега» и от отказа авторизации.

`ErrImagePull` и `ImagePullBackOff` — не два разных диагноза, а одно и то же в развитии:
первый — очередная неудачная попытка, второй — состояние ожидания между попытками. Счётчики
это и показывают: попыток скачать было 4 (`x4`), а событий backoff — 11 (`x11`), паузы между
попытками растут.

### Стадия 3: контейнер запущен и сразу упал

```
Normal   Scheduled  2m54s                default-scheduler  Successfully assigned cookbook-k8s/stuck-crashloop to k8s-volga-wk03
Normal   Pulled     78s (x5 over 2m53s)  kubelet            spec.containers{crasher}: Container image "busybox:1.37" already present on machine and can be accessed by the pod
Normal   Created    78s (x5 over 2m53s)  kubelet            spec.containers{crasher}: Container created
Normal   Started    78s (x5 over 2m53s)  kubelet            spec.containers{crasher}: Container started
Warning  BackOff    8s (x5 over 2m51s)   kubelet            spec.containers{crasher}: Back-off restarting failed container crasher in pod stuck-crashloop_cookbook-k8s(...)
```

Этот под прошёл весь путь: назначен на узел, образ получен, контейнер **создан и запущен** —
`Started` в событиях есть, причём пять раз. То есть Kubernetes сделал всё, что от него
требовалось; сломалось приложение внутри. Что именно произошло, событий уже недостаточно, но
статус пода отвечает прямо:

```
Error exitCode=1
```

`lastState.terminated` — код выхода прошлой попытки. Именно отсюда путь ведёт дальше, в логи
контейнера: причина уже не в кластере, а внутри процесса.

### Куда это ведёт в реальной диагностике

Три шага, в этом порядке: `get pod -o custom-columns=…,SCHEDULED:…,NODE:.spec.nodeName` —
понять стадию; `describe pod` — прочитать события той стадии; и только потом `logs`, и только
если под дошёл до стадии 3. Обратный порядок («сначала посмотрим логи») на стадиях 1 и 2 не
даст ничего: контейнер ещё ни разу не запускался, логов не существует.

### Уборка после демо

Залипшие поды бесполезно держат квоту namespace, поэтому фикстура заканчивается проверкой,
что после удаления счётчики вернулись к исходным:

```
Resource         Used    Hard
--------         ----    ----
limits.cpu       3       8
limits.memory    2432Mi  8Gi
pods             13      30
requests.cpu     950m    4
requests.memory  992Mi   4Gi
```

`trace-demo` из демо 1 при этом продолжает работать. Если поды демо 2 применить снова и
забыть удалить — их снимет `teardown.sh` по метке `stand: cookbook-architecture`
(`kubectl delete all …` захватывает и голые Pod).

## Ограничения и особенности

Здесь — всё, что важно не упустить при чтении демо выше, в одном месте:

1. **etcd вне Kubernetes — свойство Talos, а не Kubernetes вообще.** На kubeadm-кластере etcd
   обычно как раз статик-под в `kube-system`, и там привычное представление верно. Фикстура
   `00-control-plane.txt` фиксирует ровно наблюдаемый факт — **отсутствие** подов с `etcd` в
   имени во всех namespace, — а не заявление «etcd тут вообще нет». Где именно он работает,
   этой командой не показать: вывод доказывает только то, что kubelet им не управляет как
   подом.
2. **`-v=6` показывает запросы `kubectl`, а не внутренние вызовы контроллеров.** Всё, что
   видно в блоке «что отправляет kubectl», — трафик клиента к apiserver и не более того.
   Дальнейшая цепочка (`deployment-controller` → `replicaset-controller` → планировщик →
   kubelet) в этот лог не попадает вовсе; она восстанавливается по `ownerReferences` и
   событиям, а не по `-v=6`. Более подробные уровни (`-v=8` и выше) добавят тела запросов
   того же клиента, но чужих вызовов не покажут.
3. **События живут ограниченное время.** Поэтому картина демо 1 снята сразу после применения:
   при повторе через час часть событий уже не увидеть — apiserver вычищает их по
   `--event-ttl` (значение по умолчанию — 1 час). Это не гипотеза: последний блок
   `10-apply-path.txt` снят через 1 ч 37 мин после демо — поды `trace-demo` живы и работают
   (`.status.startTime` = `2026-07-24T08:27:57Z`), а событий их создания в namespace осталось
   ровно ноль. Восстановить картину задним числом нечем: `describe pod` в этот момент покажет
   `Events: <none>` у совершенно здорового пода.
4. **`--sort-by=.metadata.creationTimestamp` имеет разрешение в секунду.** Внутри одной
   секунды такая сортировка даёт произвольный (и потому ложный) порядок — в фикстуре шесть
   первых событий уложились в одну секунду `08:27:57Z`, и по колонке времени причинную цепочку
   не восстановить. Истинный порядок появления взят из наносекундного суффикса в имени события
   (`<объект>.<hex наносекунд>`); сверка суффикса `18c52c6dc652668c` с `.eventTime` того же
   события приведена прямо в фикстуре. Наносекунда в имени — момент **записи** события, а не
   момент самого действия.
5. **Провенанс часов: где наносекунды сравнивать можно, а где нельзя.** Первые пять событий
   (`ScalingReplicaSet`, `SuccessfulCreate` ×2, `Scheduled` ×2) эмитит один хост — обе аренды,
   и `kube-controller-manager`, и `kube-scheduler`, держит `k8s-volga-cp01`, что видно в
   `00-control-plane.txt`. Часы одни, порядок этих пяти доказан. Оговорка: аренды сняты в
   `07:55:36Z` — за полчаса до событий (`08:27:57Z`); прямо на момент события подтверждён
   только хост планировщика (`reportingInstance` = `default-scheduler-k8s-volga-cp01`),
   а для `kube-controller-manager` такого поля нет. Признаков смены лидера в промежутке
   нет: обе выборки аренды показывают один `holderIdentity` с тем же UUID.
   А `Pulled`/`Created`/`Started`
   пишут kubelet'ы разных узлов (`k8s-volga-wk03` и `k8s-volga-wk02`, поле `.source.host`):
   внутри одного пода порядок с одних часов и корректен, а **взаимный** порядок между двумя
   подами опирается на синхронность часов двух узлов и этой фикстурой не доказан.
6. **У событий `Scheduled` поле `.source.component` пусто.** Планировщик пишет события через
   `events.k8s.io/v1`, где источник лежит в `.reportingComponent`/`.reportingInstance`, а не в
   устаревшем `.source`. Контроллеры и kubelet здесь ещё пишут по-старому, поэтому у них
   `.source.component` заполнен. Практическое следствие: фильтр или колонка только по
   `.source.component` событий планировщика просто не покажет — их легко принять за
   отсутствующие.
7. **`Insufficient cpu` на этом кластере недостижим.** Потолок задаёт не `LimitRange` (его
   `max cpu: 2` — на контейнер), а `ResourceQuota` namespace: остаток `requests.cpu` —
   `3050m` (`4` минус занятые `950m`), тогда как минимум свободного на воркерах — `3120m`
   (`wk02`: `3950m` минус `830m`). Всё, что проходит квоту, помещается на любой воркер, зазора
   между «квота разрешает» и «узел вмещает» нет — запас `70m`. Поэтому `Pending` в демо 2
   получен другим предикатом той же стадии — заведомо невыполнимым `nodeSelector`. На кластере
   без квоты на `requests.cpu` тот же под честнее делать через `requests.cpu` больше
   `allocatable` любого узла. Числа здесь — снимок конкретного кластера в момент съёмки
   (включая `100m`, которые держит `trace-demo` из демо 1); на другом кластере или при другой
   загрузке арифметика будет своя, проверять её надо заново.
8. **Фактический интервал между снимками аренды — ≈32–33 с, а не 20.** В команде между
   выборками стоит `sleep 20`, но метки `renewTime` в фикстуре расходятся на `32,18` с
   (`kube-controller-manager`) и `32,15` с (`kube-scheduler`), а метки `date` — на 33 с:
   разницу дают накладные расходы на сами вызовы `kubectl`. Заголовок блока в фикстуре
   («через 20 с») называет параметр команды, измеренный интервал считается по меткам. Вывод
   демо от этого не меняется — важно, что держатель тот же, а `renewTime` продвинулось.

## Порядок запуска

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl config current-context   # должен быть admin@k8s-volga
cd kubernetes/architecture
```

### Демо 0 — только чтение, применять нечего

```bash
bash capture.sh 00-control-plane -- get nodes \
  -o custom-columns=NAME:.metadata.name,VERSION:.status.nodeInfo.kubeletVersion,OS:.status.nodeInfo.osImage,RUNTIME:.status.nodeInfo.containerRuntimeVersion,TAINTS:.spec.taints
```

Дальше в тот же файл дописываются блоки (`>> fixtures/00-control-plane.txt`): компоненты
control plane, поиск подов etcd по всем namespace, две выборки `Lease` **с `sleep 20` между
ними**, `kubectl api-versions | wc -l` и `kubectl version`. Дословные команды каждого блока —
в комментариях самой фикстуры. `capture.sh` перезаписывает файл (`tee`), поэтому вызывается он
один раз, первым, а остальные блоки идут дописыванием.

### Демо 1 — путь применения

```bash
# ВАЖНО: -v=6 снимается на ПЕРВОМ применении, когда объекта ещё нет.
kubectl apply -f manifests/10-trace-target.yaml -v=6 2>&1 | grep round_trippers
kubectl -n cookbook-k8s rollout status deploy/trace-demo

bash capture.sh 10-apply-path -- get deploy,replicaset,pod \
  -n cookbook-k8s -l stand=cookbook-architecture
# затем — сразу, пока события живы — дописать блоки >> fixtures/10-apply-path.txt:
# лог -v=6, цепочку ownerReferences, события, наносекундный порядок, узлы-источники
```

Две вещи, без которых демо не воспроизведётся:

- **`-v=6` только на первом `apply`.** Пока Deployment `trace-demo` не существует, клиент
  получает `404 Not Found` на проверочный `GET` и делает `POST … 201 Created`. На повторном
  применении того же манифеста будет `200 OK` и `PATCH` вместо `POST` — картина «одно чтение,
  одна запись на создание» пропадёт. Если Deployment уже есть, перед съёмкой его надо удалить
  (`kubectl -n cookbook-k8s delete deploy trace-demo`) и дождаться исчезновения подов.
- **Съёмка событий — сразу после `rollout status`.** События вычищаются по `--event-ttl`
  (по умолчанию час), см. пункт 3 ограничений; отложенная на час съёмка вернёт пустой список.

`apply.sh` (`bash apply.sh manifests/10-trace-target.yaml`) делает то же применение с проверкой
контекста, но без `-v=6` — он годится для повторного развёртывания стенда, не для съёмки
фикстуры.

### Демо 2 — три места залипания

Запускается **после** демо 1 и при живом `trace-demo`: арифметика квоты в фикстуре
(`requests.cpu` занято `950m`) включает `100m`, которые держат две реплики `trace-demo`. Если
снять демо 2 на пустом namespace, числа будут другими и разбор «`Insufficient cpu`
недостижим» придётся пересчитывать.

```bash
for m in 20-stuck-pending 21-stuck-imagepull 22-stuck-crashloop; do
  bash apply.sh manifests/$m.yaml
done

sleep 180   # нужно, чтобы накопились restartCount и backoff:
            # в фикстуре x4 попытки скачивания, x11 backoff, x5 запусков за ~2m53s

bash capture.sh 20-stuck -- get pod -n cookbook-k8s stuck-pending stuck-imagepull stuck-crashloop \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,REASON:.status.containerStatuses[0].state.waiting.reason,RESTARTS:.status.containerStatuses[0].restartCount,NODE:.spec.nodeName
# дописать >> fixtures/20-stuck.txt: describe по каждому поду (блок Events),
# стадии (PodScheduled/nodeName/Ready), lastState.terminated

kubectl -n cookbook-k8s delete pod stuck-pending stuck-imagepull stuck-crashloop
kubectl describe quota -n cookbook-k8s | tail -8 >> fixtures/20-stuck.txt   # блок «после уборки»

# ТОЛЬКО ПОСЛЕ УБОРКИ: разбор недостижимости Insufficient cpu
# (allocatable, requests узлов, LimitRange, quota) — снимок квоты и загрузки узлов
# берётся уже без подов демо 2; в файле этот блок стоит выше «после уборки»,
# но цитирует тот же снимок квоты (об этом сказано в самой фикстуре).
```

Поды демо 2 удаляются сразу после съёмки: залипший под держит квоту namespace и мешает
соседним стендам. `trace-demo` при этом остаётся жить — на него ссылаются демо 1 и статья.

**Почему разбор квоты снимается именно после уборки.** Три пода демо 2 держат по
`requests.cpu 10m` и три места в счётчике `pods`. Снимок при живых `stuck-*` дал бы
`requests.cpu 980m`, `pods 16` и остаток квоты `3020m` — а в фикстуре и в статье
`950m`, `13` и `3050m`. Снимок загрузки узлов (`describe node`) — по той же причине
и в тот же момент: два из трёх подов демо 2 сидят на `wk03`.

## Teardown

```bash
bash teardown.sh
```

Удаляет из namespace `cookbook-k8s` объекты **строго по метке** `stand: cookbook-architecture`
— `all` (Deployment, ReplicaSet, Pod, Service, StatefulSet…), `cm`, `secret`, `sa`, `role`,
`rolebinding`. На момент финализации стенда под метку попадают ровно четыре объекта: Deployment
`trace-demo`, его ReplicaSet и два пода; проверено `--dry-run=client`. Голые поды демо 2, если
их применили и забыли удалить, снимаются тем же проходом (`kubectl delete all …` захватывает и
Pod без контроллера).

Чего `teardown.sh` не делает:

- **Namespace `cookbook-k8s` не удаляет** — он общий для всех стендов серии.
- **Чужие объекты в том же namespace не трогает** — стенды `resources`, `autoscaling`,
  `stateful`, `secrets`, `networking`, `operators` живут рядом со своими метками
  (`stand: cookbook-secrets`, `stand: cookbook-operators` и т. д.) и под селектор не попадают.
- **В `kube-system` и на узлах не делает ничего вообще** — этому стенду там нечего удалять, он
  их только читал: ни статик-подов, ни `Lease`, ни тейнтов, ни меток узлов стенд не создавал и
  не менял.

Скрипт идемпотентен: `--ignore-not-found` позволяет запускать его повторно на уже пустом
стенде. Перед удалением он проверяет контекст (`admin@k8s-volga`) и отказывается работать в
чужом кластере.
