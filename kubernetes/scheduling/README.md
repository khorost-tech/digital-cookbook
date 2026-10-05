# Стенд: планирование подов — фильтрация, скоринг, тейнты и приоритеты (k8s-volga)

Живые замеры для серии «Kubernetes на практике»: как `default-scheduler` выбирает узел для
пода. Стенд продолжает `../architecture` ровно с того места, где тот закончился: там видно,
что решение о размещении принял `default-scheduler` (экземпляр
`default-scheduler-k8s-volga-cp01`) — здесь разбирается, как он это решение принимает.

## Назначение

Показать выбор узла не как «свободное место», а как то, чем он является на самом деле:
двухфазную процедуру (сначала фильтрация — какие узлы вообще подходят, потом скоринг — какой
из подходящих лучше), поверх которой работают тейнты и толерации, ограничения размещения
(`nodeSelector`, affinity, topology spread) и приоритеты с вытеснением.

Главный факт стенда, с которого всё начинается: **планировщик считает по `requests`, а не по
фактическому потреблению.** Число, на которое он смотрит, и то, что поды едят на самом деле,
расходятся по CPU в десятки раз. Это прямой стык со статьёй 2 серии про ресурсы: там
`requests` объяснялись как «заявка», здесь видно, во что эта заявка обходится на живом узле.

Второй факт, без которого первый легко посчитать неправильно: **`describe node` и
`kubectl top node` отвечают на разные вопросы** — первый суммирует `requests` подов, второй
показывает потребление узла целиком. Сопоставлять с `requests` можно только сумму
`kubectl top pod` по подам узла; как эти три числа соотносятся — ниже в демо 0.

## Требования к кластеру

- Kubernetes 1.36+.
- Доступ **на чтение** к узлам (`get`/`list` узлов, `describe node`) и к `kube-system`
  (`get pod` — оттуда берётся контрпример с DaemonSet-толерациями `cilium` в демо 1, и оттуда
  же считается срез толераций по всем namespace). В обоих местах стенд только читает: исходная
  картина снимается без единой мутации.
- **`metrics.k8s.io`** (на k8s-volga его обслуживает `kube-system/metrics-server`) — иначе
  ни `kubectl top node`, ни `kubectl top pod` не отдадут фактическое потребление, и половина
  главного сравнения «запрошено против факта» окажется недоступна. Проверка: `kubectl top
  nodes` и `kubectl top pod -A` должны вернуть таблицу, а не ошибку.
- **Несколько worker-узлов, минимум три** (на k8s-volga их ровно три) — иначе фильтрация и
  скоринг вырождаются: выбирать не из чего. Трёх требуют именно демо 2: pod anti-affinity
  раскладывает три реплики по трём узлам и упирает четвёртую в `Pending`, а topology spread
  даёт `2+1+1` — на двух воркерах ни то, ни другое не воспроизводится.
- **Хотя бы один тейнт в кластере, который стенд может использовать, не создавая своих.** На
  k8s-volga это штатный `node-role.kubernetes.io/control-plane:NoSchedule` на трёх
  control-plane узлах. Стенд свои тейнты не ставит (см. «Изоляция и безопасность»).
- Права **`create`/`delete`** на Pod и Deployment в namespace `cookbook-k8s` и — для демо
  вытеснения — на **кластерные `PriorityClass`**: это объект вне namespace, прав на namespace
  для него не хватит. Ничего другого стенд не создаёт: ни PVC, ни Service, ни ConfigMap.
- Право **ставить и снимать лейбл на узле** (`kubectl label node`): демо ограничений
  размещения ставит **ровно одну** метку с префиксом стенда на **один** worker
  (`manifests/00-node-label.sh set`), а `teardown.sh` снимает её.
- Namespace `cookbook-k8s` уже существует (создан стендом `../resources`, общий для всех
  стендов серии) — этот стенд свой namespace не создаёт.

Чего кластер **не обязан** уметь: топологических зон стенду не требуется — их на k8s-volga и
нет, и это отдельно разобрано в «Ограничениях и особенностях» (пункт 1).

## Таблица версий

Все значения — из `fixtures/00-scheduler.txt`, блок «версии»; образ виден в фикстурах демо,
в тексте событий kubelet.

| Компонент | Версия | Откуда |
|---|---|---|
| Kubernetes (сервер) | `v1.36.2` | `00-scheduler.txt`, `Server Version` |
| Kubernetes (клиент `kubectl`) | `v1.36.1` | `00-scheduler.txt`, `Client Version` |
| Kustomize (в составе `kubectl`) | `v5.8.1` | `00-scheduler.txt`, `Kustomize Version` |
| kubelet на всех шести узлах | `v1.36.2` | `00-scheduler.txt`, колонка `VERSION` |
| ОС узлов | Talos `v1.13.6` | `00-scheduler.txt`, колонка `OS` |
| Среда исполнения | `containerd://2.2.5` | `00-scheduler.txt`, колонка `RUNTIME` |
| Поставщик `metrics.k8s.io` | `kube-system/metrics-server`, `available=True` | `00-scheduler.txt`, блок про `apiservice` |
| Образ всех подов стенда | `registry.k8s.io/pause:3.10` | `10-taint.txt`, событие `Pulled`; `40-preemption.txt`, событие `Pulled` |

Клиент на патч-версию младше сервера (`v1.36.1` против `v1.36.2`) — штатная ситуация, на
вывод демо не влияет.

## Изоляция и безопасность

Кластер k8s-volga — **общий и живой**: на каждом из трёх воркеров уже работает два десятка
подов — `Non-terminated Pods: (20 in total)` снято по `wk01`, `wk02` и `wk03` (фикстура, блок
«сколько подов на каждом воркере»). Это системные компоненты (Cilium, CoreDNS, CSI, vector),
ArgoCD, cert-manager, external-secrets, KEDA — и объекты соседних стендов серии, которые
трогать нельзя ровно так же. Планирование — как раз та тема,
где демо легче всего задеть соседей, поэтому границы жёсткие.

- **Своих тейнтов стенд НЕ ставит.** Тейнт на общем воркере повлиял бы на планирование всего
  кластера, а не только демо-подов. Для демо «под не садится, пока нет толерации»
  используется **уже существующий** штатный тейнт
  `node-role.kubernetes.io/control-plane:NoSchedule` на control-plane узлах — он в кластере и
  так есть, стенд его только читает и терпит.
- **Узлы НЕ дренируются.** `kubectl drain`, `cordon`, `uncordon` в стенде запрещены и ни в
  одном скрипте не встречаются: любой из них вытеснил бы или заблокировал чужие рабочие
  нагрузки.
- **Метка на узле — ровно одна.** `cookbook.khorost.tech/scheduling=demo`, с префиксом
  стенда, на **одном** worker-узле, ставится `manifests/00-node-label.sh set` и снимается
  `teardown.sh` (а также `00-node-label.sh unset`). Имя узла в скрипте не захардкожено:
  берётся первый узел без тейнта control-plane, и выбранное имя печатается на экран. Никаких
  системных меток (`kubernetes.io/*`, `node-role.kubernetes.io/*`, метки CNI и операторов)
  стенд не трогает. Метка **переживает демо 2 и 3**: её ставит демо 2, демо 3 опирается на
  существование её ключа, демо 4 приколачивает по ней всех своих подов к одному узлу —
  поэтому уборка демо 2 удаляет только объекты в namespace, а снимается метка в конце демо 4.
  Единственное, что стенд может оставить за собой при обрыве на середине, — эта метка; она
  безобидна (лейбл сам по себе на планирование чужих подов не влияет), но `teardown.sh` в
  конце обязателен. По факту метка снята: в конце `fixtures/40-preemption.txt`
  `kubectl get nodes -l cookbook.khorost.tech/scheduling -o name` не выводит ничего.
- **`PriorityClass` — без `globalDefault`.** Оба класса стенда (`cookbook-low`,
  `cookbook-high`) объявляются без `globalDefault: true`: класс с этим флагом стал бы
  умолчанием для **всех** подов кластера, у которых приоритет не указан явно. Значения
  приоритета берутся заведомо ниже системных (`system-cluster-critical` = `2000000000`,
  `system-node-critical` = `2000001000`), чтобы демо вытеснения не могло дотянуться до
  системных подов. Контроль в фикстуре: `kubectl get priorityclass -o json | grep -c
  '"globalDefault": true'` → `0` — ни одного класса по умолчанию в кластере нет. Объекты
  кластерные, поэтому `teardown.sh` удаляет их отдельным шагом; по факту оба удалены — в
  конце `fixtures/40-preemption.txt` `kubectl get priorityclass` отдаёт только два системных.
- **Control plane — только чтение.** Против `kube-system`, статик-подов и конфигурации Talos
  стенд выполняет только `kubectl get`/`describe`/`top`.
- **Демо-объекты — только в существующем namespace `cookbook-k8s`**, все с меткой
  `stand: cookbook-scheduling`. Namespace общий для стендов серии, поэтому чужие объекты в нём
  под селектор уборки не попадают.
- **Что не ломаем:** стенды фаз 1–3 (`resources`, `autoscaling`, `stateful`, `secrets`,
  `networking`, `operators`) и стенд `architecture` (Deployment `trace-demo`) — они живут в
  том же namespace со своими метками.

## Демо

- **Демо 0: исходная картина и главный факт** — `fixtures/00-scheduler.txt`. Тейнты и
  `allocatable` всех шести узлов, отсутствие зон, и сравнение `requests` с фактическим
  потреблением подов на трёх воркерах — на сопоставимых величинах, с отдельным разбором того,
  почему `kubectl top node` в это сравнение не входит.
- **Демо 1: тейнт — первый фильтр** — `manifests/10-taint-toleration.yaml`,
  `fixtures/10-taint.txt`. Два одинаковых пода, прибитых к одному и тому же control-plane
  узлу; разница между ними — только толерация. Своих тейнтов не ставилось: использован
  штатный `node-role.kubernetes.io/control-plane:NoSchedule`. Оба пода удалены сразу после
  съёмки фикстуры.
- **Демо 2: куда сядет под** — `manifests/20-node-affinity.yaml`,
  `manifests/30-pod-antiaffinity.yaml`, `manifests/40-topology-spread.yaml`,
  `manifests/41-topology-spread-honor.yaml`, `fixtures/20-placement.txt`. Три способа
  управлять размещением: притянуть под к узлу (node affinity), развести реплики по узлам
  (pod anti-affinity), выровнять распределение (topology spread). Здесь же ставится метка
  узла — и, в отличие от остальных демо, **она остаётся** после демо: её используют демо 3 и 4.
  Все поды и Deployment'ы демо удалены сразу после съёмки фикстуры.
- **Демо 3: вечный Pending** — `manifests/50-stuck-pending.yaml`, `fixtures/30-pending.txt`.
  Жёсткое `nodeAffinity` по значению метки, которого нет ни на одном узле. Текст отказа
  оказывается **байт в байт** тем же, что у совсем иначе устроенного пода из демо 1 — отсюда
  разбор того, что на самом деле считают счётчики в `FailedScheduling`. Под удалён сразу после
  съёмки фикстуры.
- **Демо 4: вытеснение** — `manifests/60-priority-classes.yaml`,
  `manifests/61-preemption.yaml`, `fixtures/40-preemption.txt`. Четыре жертвы с **отрицательным**
  приоритетом занимают узел, вытесняющий под с приоритетом `1000` не помещается — планировщик
  выселяет ровно одну жертву, минимально необходимую. Демо идёт по оси `ephemeral-storage`
  (почему — отдельный подраздел) и снабжено доказательством, что ни один чужой под на узле не
  пострадал. Здесь же снимается метка узла: это последнее демо стенда.

## Демо 0: исходная картина и «планировщик считает по requests»

Фикстура: `fixtures/00-scheduler.txt`. Мутаций нет — весь блок снимается чтением.

### Узлы, тейнты, allocatable

Шесть узлов: три control-plane с тейнтом
`node-role.kubernetes.io/control-plane:NoSchedule` и `allocatable` `1950m` CPU /
`3360936Ki` памяти, и три воркера **без тейнтов** с `3950m` CPU / `≈7613952Ki` памяти.
Тейнт на control-plane отсекает эти три узла раньше прочих проверок (доказательство —
разбивка причин отказа в демо 1): обычный под без толерации их не увидит вовсе, и выбор
фактически идёт из трёх воркеров, а не из шести узлов.

`allocatable` — не вся ёмкость узла: у `k8s-volga-wk02` `capacity` `cpu=4` и
`mem=8109576Ki`, а `allocatable` — `3950m` и `7613960Ki`. Разницу (`50m` CPU и около
`484Mi` памяти) резервирует kubelet под системные нужды, и планировщик её не раздаёт.

### Три числа и три разных вопроса

Прежде чем что-то с чем-то сравнивать, надо развести источники. Их регулярно смешивают, и из
смешения рождаются неверные выводы:

| Источник | Что показывает | На какой вопрос отвечает |
|---|---|---|
| `describe node` → `Allocated resources` | сумму `requests` подов узла | сколько **заявлено** подами |
| сумма `kubectl top pod` по подам узла | working set самих подов | сколько поды **едят** |
| `kubectl top node` | working set узла целиком: поды **плюс** kubelet, containerd, Talos, сетевой стек ядра и page cache | сколько ест **узел** |

С `requests` сопоставима только средняя строка. `kubectl top node` — величина из другой
области измерения: он включает то, чего в `Allocated resources` нет по определению. Ставить
его в одну таблицу с `requests` как «факт» нельзя, вычитать одно из другого — тоже.

Технический момент: `kubectl top pods -A --field-selector spec.nodeName=…` сервер отвергает
(`"spec.nodeName" is not a known field selector: only "metadata.name", "metadata.namespace"` —
дословно в фикстуре), поэтому список подов узла берётся из core API и джойнится с metrics API
по паре `namespace/имя`. Полный список подов и подсчёт суммы — в фикстуре, блок «второй
замер».

### Главный факт: по CPU считается запрошенное, а не потреблённое

`k8s-volga-wk02`, все три величины из одного замера:

| Что | CPU | Откуда |
|---|---|---|
| Запрошено (`requests` 20 подов) | `830m` (21%) | `describe node`, `Allocated resources` |
| Едят сами поды (Σ `top pod`, 20 подов) | `35m` | `sum_cpu=35m` в блоке «второй замер» |
| Ест узел целиком (`top node`) | `68m` (1%) | `kubectl top nodes` |

Сопоставимая пара — первая и вторая строки: **разрыв больше чем в двадцать раз** (`830m`
против `35m`). Узел занят на 21% с точки зрения планировщика и на `1%` с точки зрения ядра.
Свободными для нового пода планировщик считает `3120m` (`3950m − 830m`), хотя по факту почти
вся мощность узла простаивает. Это и есть механика `Pending` при незагруженном кластере.

Картина одинакова на всех трёх воркерах:

| Узел | `requests` CPU | Σ `top pod` | Σ / `requests` |
|---|---|---|---|
| `k8s-volga-wk01` | `780m` (19%) | `21m` | ≈ 1/37 |
| `k8s-volga-wk02` | `830m` (21%) | `35m` | ≈ 1/24 |
| `k8s-volga-wk03` | `630m` (15%) | `33m` | ≈ 1/19 |

### Память: запрошена с запасом, недооценки нет ни на одном узле

Тот же замер, та же пара сопоставимых величин:

| Узел | `requests` памяти | Σ `top pod` | Σ / `requests` | `top node` (для сравнения) |
|---|---|---|---|---|
| `k8s-volga-wk01` | `1030Mi` (13%) | `488Mi` | ≈ 0,47 | `1329Mi` (17%) |
| `k8s-volga-wk02` | `1254Mi` (16%) | `610Mi` | ≈ 0,49 | `1214Mi` (16%) |
| `k8s-volga-wk03` | `754Mi` (10%) | `644Mi` | ≈ 0,85 | `1293Mi` (17%) |

Ни на одном воркере поды не потребляют больше, чем запросили: `0,47`–`0,85` от заявки. По
памяти `requests` здесь выставлены **с запасом**, и никакой «недооценки» замер не показывает.
Разрыв в разы, как по CPU, — только по CPU.

### Почему `top node` вдвое больше суммы `top pod`

Разница на каждом воркере: `1329 − 488 = 841Mi` (`wk01`), `1214 − 610 = 604Mi` (`wk02`),
`1293 − 644 = 649Mi` (`wk03`) — то есть `top node` в 2,0–2,7 раза больше суммы по подам.

Это **не** «поды съели больше, чем видно в `top pod`». Это непододовый расход самого узла:
kubelet, containerd, компоненты Talos, сетевой стек ядра, page cache образов и логов. Часть
его kubelet резервирует заранее — именно поэтому `allocatable` меньше `capacity` на `50m` CPU
и ≈`484Mi` памяти, — но резерв это план, а `top node` факт, и совпадать они не обязаны.

Практический вывод, ради которого стенд и снимал третье число: **`requests`, Σ `top pod` и
`top node` — ответы на разные вопросы, и арифметика между ними бессмысленна.** «Правильно ли
выставлены `requests`» — это `requests` против Σ `top pod`. «Не пора ли добавить узел» — это
`top node` против ёмкости узла. «Влезет ли ещё один под» — это сумма `requests` против
`allocatable`, и только она, потому что планировщик не смотрит ни на один `top`.

### Оговорка про «недооценённый requests — это отложенный OOM»

Формулировка ходовая, но механически неточная, и на этом кластере она к тому же не про что:
недооценки по памяти замер не показал. По механике:

- **OOM-kill контейнера срабатывает по `limits`** (cgroup `memory.max`), а не по `requests`.
  Под без лимита памяти по своему `requests` убит не будет.
- **`requests` влияют на размещение** — планировщик складывает их и сравнивает с
  `allocatable`.
- **И на порядок вытеснения**: при нехватке памяти на узле kubelet выселяет сначала
  `BestEffort` и `Burstable`-поды, потребляющие сверх своих `requests`, `Guaranteed` —
  последними. Заниженный `requests` по памяти означает «выселят первым», а не «убьёт сразу».

### Лимиты: переподписка допустима

Отдельная строка `describe node` — про лимиты: `limits` CPU на `wk02` равны `4500m` (113%)
при `allocatable` `3950m`. Сумма лимитов **превышает** ёмкость узла, и сам `kubectl` об этом
предупреждает: `(Total limits may be over 100 percent, i.e., overcommitted.)`. Переподписка по
лимитам допустима именно потому, что планировщик считает не по ним.

### Метки времени

Блоки снимались подряд, но не одномоментно; `kubectl top` отдаёт скользящее среднее, поэтому
в фикстуре по `wk02` встречаются `68m` и `72m` — это дрейф живой нагрузки между вызовами, а не
расхождение методик. `requests` за то же время не менялись: они статичны, пока не создан или
не удалён под — во втором замере `describe node` по всем трём воркерам вернул те же значения,
что и в первом. Сравнения выше построены на числах **второго** замера, где сумма `top pod`,
`top node` и `requests` сняты в один проход.

## Демо 1: тейнт — первый фильтр

Манифест: `manifests/10-taint-toleration.yaml`. Фикстура: `fixtures/10-taint.txt`.

### Постановка: два одинаковых пода и один узел

Оба пода — `registry.k8s.io/pause:3.10`, `requests` `cpu: 10m` / `memory: 16Mi`,
`limits` `cpu: 50m` / `memory: 64Mi` (проходят `LimitRange cookbook-limits` — по фикстуре
`max` там `cpu: 2` и `memory: 2Gi`, а `min` пуст, то есть нижней границы нет). Оба прибиты `nodeSelector`
`kubernetes.io/hostname: k8s-volga-cp02` к **одному и тому же** узлу. Единственное
различие — толерация:

```yaml
tolerations:
  - key: node-role.kubernetes.io/control-plane
    operator: Exists
    effect: NoSchedule
```

Ключ и эффект взяты не по памяти, а из живого кластера — `fixtures/00-scheduler.txt`
показывает на всех трёх control-plane узлах ровно один тейнт:
`[map[effect:NoSchedule key:node-role.kubernetes.io/control-plane]]`, без значения. Поэтому
`operator: Exists` — корректная форма: она сопоставляется по ключу, значения у тейнта нет.

### Результат: один узел, два исхода

```
NAME              PHASE     NODE             SCHEDULED
no-toleration     Pending   <none>           PodScheduled
with-toleration   Running   k8s-volga-cp02   PodReadyToStartContainers
```

Колонка `SCHEDULED` здесь — это `.status.conditions[0].type`, то есть просто **первое**
условие в списке, а не ответ «размещён ли под». У `Pending`-пода первое условие называется
`PodScheduled`, у запущенного список начинается с `PodReadyToStartContainers`. Сам
**статус** условия эта колонка не показывает — в неё выбран только `.type`, и в фикстуре
статуса нет. Настоящий сигнал в этой таблице — пара `PHASE` + `NODE`.

### Точный текст отказа

```
Events:
  Type     Reason            Age   From               Message
  ----     ------            ----  ----               -------
  Warning  FailedScheduling  58s   default-scheduler  0/6 nodes are available: 3 node(s) didn't match Pod's node affinity/selector, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 6 Preemption is not helpful for scheduling.
```

Разбор, который планировщик сделал сам: из шести узлов **три** отвалились по
`node affinity/selector` — это воркеры, их `kubernetes.io/hostname` не равен
`k8s-volga-cp02`; ещё **три** отвалились по нетерпимому тейнту — это все control-plane узлы,
включая тот единственный, который под и просил. Ни одного кандидата не осталось.

### Разбивка 3/3 доказывает, что тейнт сработал раньше селектора

Эта арифметика — не украшение, из неё следует порядок фильтров. Селектору
`kubernetes.io/hostname: k8s-volga-cp02` не соответствуют **пять** узлов: три воркера плюс
`cp01` и `cp03`. Если бы каждому узлу писалась «любая подходящая» причина, в корзине
`didn't match Pod's node affinity/selector` их могло оказаться до пяти. А там ровно **три**.
И в корзине тейнта тоже **три** — при этом тейнтованных узлов в кластере ровно три
(контрольный `kubectl get nodes` в конце `fixtures/10-taint.txt`), других кандидатов в эту
корзину просто нет. Значит она целиком состоит из `cp01`, `cp02`, `cp03`.

Отсюда вывод: `cp01` и `cp03` проваливают **обе** проверки — и селектор, и тейнт, — но
посчитаны только в корзине тейнта. Планировщик записывает на узел ровно **одну** причину —
ту, что сработала первой. Раз для `cp01` и `cp03` ею оказался тейнт, значит
`TaintToleration` отработал **раньше** `NodeAffinity`. Порядок виден прямо из одного
события, без чтения кода планировщика.

Оговорка: это утверждение про **этот** кластер и **эту** версию (k8s 1.36.2, штатный
`default-scheduler`). Порядок плагинов задаётся профилем планировщика и в принципе
настраивается; стенд показывает фактический порядок, а не гарантию API.

Два момента в этом тексте стоит прочесть буквально:

- **Тейнт не назван.** Сообщение говорит `3 node(s) had untolerated taint(s)` — без ключа,
  без значения, без эффекта. Имя тейнта в этом стенде известно из `kubectl get nodes`
  (`fixtures/00-scheduler.txt`), а не из события. На k8s 1.36.2 по одному только
  `FailedScheduling` понять, *какой именно* тейнт помешал, нельзя — придётся идти в описание
  узлов.
- **Вытеснение не поможет.** Хвост `preemption: 0/6 nodes are available: 6 Preemption is not
  helpful for scheduling.` — планировщик отдельно проверил, спасёт ли выселение чужих подов,
  и ответил «нет» по всем шести узлам. Читать это надо строго так: причина отказа такая, что
  освобождением места она не лечится. Дело не в нехватке ресурсов — выселяй хоть весь узел,
  тейнт и селектор от этого не изменятся. Про **порядок** запуска плагинов хвост
  `preemption` не говорит ничего: порядок в этом стенде доказан разбивкой 3/3 выше, а не
  этой строкой.

Заметьте, чего в замере **нет**: он не показывает, считались ли вообще `requests` пода.
Под просит `10m` CPU и `16Mi` памяти при `allocatable` узла `cp02` в `1950m` и `3360936Ki`
(`fixtures/00-scheduler.txt`) — на таком фоне ресурсы не были ограничением ни при каком
порядке проверок, и замер это подтвердить не мог. Утверждение «до расчётов по
`requests` дело не доходит» — про устройство планировщика по умолчанию, и **этим стендом
оно не измерено**.

Под с толерацией на том же узле:

```
  Normal  Scheduled  58s   default-scheduler  Successfully assigned cookbook-k8s/with-toleration to k8s-volga-cp02
  Normal  Pulling    58s   kubelet            spec.containers{pause}: Pulling image "registry.k8s.io/pause:3.10"
  Normal  Pulled     58s   kubelet            spec.containers{pause}: Successfully pulled image "registry.k8s.io/pause:3.10" in 634ms (634ms including waiting). Image size: 320368 bytes.
```

### Толераций у пода больше, чем написал автор

Список толераций уже созданного `with-toleration` — не тот, что в манифесте:

```
node-role.kubernetes.io/control-plane op=Exists effect=NoSchedule tolerationSeconds=
node.kubernetes.io/not-ready op=Exists effect=NoExecute tolerationSeconds=300
node.kubernetes.io/unreachable op=Exists effect=NoExecute tolerationSeconds=300
```

Своя в манифесте была **одна**, в объекте их **три**. Две нижние дописал admission-плагин
`DefaultTolerationSeconds` — они появляются у любого пода, который **сам не объявил**
толерацию на эти ключи. У соседнего пода `demo-0` из того же namespace
`kubectl get pod demo-0 -o jsonpath='{.spec.tolerations}'` возвращает ровно эту пару и
больше ничего.
Смысл у них практический: под не выселяется с узла мгновенно, как только узел стал
`NotReady` или `Unreachable`, — у него есть 300 секунд на то, чтобы узел вернулся.

### Кому 300 секунд не дописывают

Слово «сам не объявил» здесь не формальность. Плагин не трогает ключ, который под уже
терпит, а DaemonSet-поды несут зонтичную толерацию `{"operator":"Exists"}` плюс явные
толерации на те же `not-ready` и `unreachable` — но **без** `tolerationSeconds`. Живой
пример из фикстуры, под `cilium` в `kube-system`:

```
[{"operator":"Exists"},{"effect":"NoExecute","key":"node.kubernetes.io/not-ready","operator":"Exists"},{"effect":"NoExecute","key":"node.kubernetes.io/unreachable","operator":"Exists"}, …]
```

Ключи те же, срока нет. Отсутствие `tolerationSeconds` означает «терпеть бессрочно»: такой
под не уедет с узла, который стал `NotReady`, ни через 300 секунд, ни через час.

Так и задумано. Cilium — это сеть узла, `vector` — сбор логов, `csi-nfs-node` — монтирование
томов. Если узел моргнул, эти поды нужны на нём **особенно**: они и есть то, что вернёт узел
в строй и покажет, что там произошло. Выселять их по таймеру — значит гарантированно терять
и связность, и телеметрию ровно в тот момент, когда они нужнее всего.

Масштаб исключения виден по срезу всего кластера (фикстура, один проход
`kubectl get pod -A` по всем namespace): из **82** подов пару с `tolerationSeconds: 300`
имеют **58**, а у **24** её нет. Эти 24 — четыре набора DaemonSet: `cilium`,
`cilium-envoy`, `csi-nfs-node` и `vector`, по шесть подов на шесть узлов. То есть «пара с
300 секундами» — про обычную рабочую нагрузку, а не про весь кластер.

Толерация при этом **снимает запрет, но не притягивает**: она разрешает разместиться на
узле с соответствующим тейнтом, а не заставляет планировщик его выбрать. В демо под попал
именно на control-plane узел только потому, что был явно прибит `nodeSelector`. Отдельного
замера «куда сел бы под с толерацией, но без селектора» в этом стенде нет — на общем
кластере такой под мог бы приземлиться на боевой control-plane узел, и запускать его ради
иллюстрации не стали.

### Что стенд не трогал и что за собой убрал

- **Своих тейнтов не ставилось и чужих не снималось.** Использован уже существующий штатный
  тейнт `node-role.kubernetes.io/control-plane:NoSchedule`. Контрольный снимок после демо
  (в конце фикстуры) показывает те же тейнты на тех же узлах, что и до него: три
  control-plane с `NoSchedule`, три воркера с `<none>`. Лейблы узлов в этой задаче тоже не
  трогались.
- **Под с control-plane узла удалён сразу после съёмки.** `kubectl get pod -A
  --field-selector spec.nodeName=k8s-volga-cp02 --no-headers | grep -c cookbook` даёт `0`, а
  `kubectl get pod -n cookbook-k8s -l stand=cookbook-scheduling` — `No resources found`.
  Под прожил на боевом узле считанные минуты — ровно столько, сколько снималась фикстура, —
  и всё это время держал запрошенными `10m` CPU и `16Mi` памяти.
- `kubectl apply` печатает предупреждение PodSecurity про профиль `restricted` (у `pause`
  нет `securityContext`). Дословный текст снят отдельно, через `--dry-run=server` — этот
  режим прогоняет манифест через admission на сервере, но ничего не сохраняет; контрольный
  `kubectl get pod -l stand=cookbook-scheduling` сразу после него отдал
  `No resources found`:

  ```
  Warning: would violate PodSecurity "restricted:latest": allowPrivilegeEscalation != false (container "pause" must set securityContext.allowPrivilegeEscalation=false), unrestricted capabilities (container "pause" must set securityContext.capabilities.drop=["ALL"]), runAsNonRoot != true (pod or container "pause" must set securityContext.runAsNonRoot=true), seccompProfile (pod or container "pause" must set securityContext.seccompProfile.type to "RuntimeDefault" or "Localhost")
  pod/no-toleration created (server dry run)
  pod/with-toleration created (server dry run)
  ```

  Ключевое слово — `would violate`, и профиль в кавычках `restricted`, а не тот, что
  применяется. В namespace `cookbook-k8s` включён `pod-security.kubernetes.io/enforce:
  baseline`, ему манифест соответствует, оба пода создаются. `restricted` тут только
  предупреждает.

## Демо 2: куда сядет под

Манифесты: `manifests/20-node-affinity.yaml`, `manifests/30-pod-antiaffinity.yaml`,
`manifests/40-topology-spread.yaml`, `manifests/41-topology-spread-honor.yaml`.
Фикстура: `fixtures/20-placement.txt`.

Демо 1 отвечало на вопрос «пустят ли под на узел». Здесь вопрос другой: **где именно** он
окажется, если пустят везде. Три инструмента, три разных ответа — и один из них на этом
кластере повёл себя не так, как ожидалось.

### Метка на узле: ровно одна, и она остаётся

`manifests/00-node-label.sh set` выбрал первый узел без тейнта control-plane и пометил его:

```
NAME             LABEL
k8s-volga-wk01   demo
```

Метка одна — `cookbook.khorost.tech/scheduling=demo` на `k8s-volga-wk01`. В отличие от
остальных демо стенда, **после демо 2 она не снимается**: её используют демо 3 (вечный
`Pending` строится на том, что ключ метки существует, а требуемого значения нет) и демо 4
(все его поды приколоты `nodeSelector`'ом по этой метке к одному узлу). Снимается метка в
конце демо 4 — командой `00-node-label.sh unset` или `teardown.sh` (шаг 2).

### Node affinity: под садится именно на помеченный узел

```
affinity-demo   Running   k8s-volga-wk01
```

Под `affinity-demo` не указывал узла ни именем, ни `nodeSelector` — только правило:

```yaml
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: cookbook.khorost.tech/scheduling
              operator: In
              values: [demo]
```

Из трёх воркеров метка есть у одного, и планировщик выбрал именно его.

**Почему `nodeAffinity`, а не `nodeSelector`.** Тот же результат даёт одна строка
`nodeSelector: {cookbook.khorost.tech/scheduling: demo}` — но `nodeSelector` умеет ровно одно:
точное равенство «ключ = значение», и несколько пар в нём соединяются только по И.
У `nodeAffinity` есть операторы `In`, `NotIn`, `Exists`, `DoesNotExist`, `Gt`, `Lt`, список
значений в одном терме (`values` — это ИЛИ) и несколько альтернативных `nodeSelectorTerms`
(между термами ИЛИ, между `matchExpressions` внутри терма И). Плюс мягкая форма
`preferredDuringSchedulingIgnoredDuringExecution` с весами — «желательно, но не обязательно»,
которой у `nodeSelector` нет вовсе. В демо взята жёсткая форма: без подходящего узла под
остался бы `Pending`.

Вторая половина имени — `IgnoredDuringExecution` — про то, чего замер не показывает:
правило проверяется **при размещении**. Если метку снять с узла позже, уже запущенный под
никуда не уедет. Отдельного замера на это в стенде нет.

### Pod anti-affinity: три реплики на трёх узлах

Deployment `spread-demo`, три реплики, правило «в одном домене
(`topologyKey: kubernetes.io/hostname`, то есть на одном узле) не больше одного пода с меткой
`app: spread-demo`»:

```
spread-demo-5644ff5cbb-4ps4w   Running   k8s-volga-wk02
spread-demo-5644ff5cbb-m9kj8   Running   k8s-volga-wk03
spread-demo-5644ff5cbb-z9p9x   Running   k8s-volga-wk01
```

Три пода — три разных воркера, ни одного повтора. Обратите внимание: `wk01` тут обычный
кандидат наравне с остальными, метка демо на размещение `spread-demo` не влияет — правило
про **соседей**, а не про свойства узла.

### Цена жёсткого правила: четвёртой реплике места нет

`kubectl -n cookbook-k8s scale deploy spread-demo --replicas=4`:

```
spread-demo-5644ff5cbb-2skfx   Pending   <none>
spread-demo-5644ff5cbb-4ps4w   Running   k8s-volga-wk02
spread-demo-5644ff5cbb-m9kj8   Running   k8s-volga-wk03
spread-demo-5644ff5cbb-z9p9x   Running   k8s-volga-wk01
```

Дословный отказ:

```
  Warning  FailedScheduling  49s   default-scheduler  0/6 nodes are available: 3 node(s) didn't match pod anti-affinity rules, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 3 No preemption victims found for incoming pod, 3 Preemption is not helpful for scheduling.
```

Снова разбивка 3/3, но причины другие, чем в демо 1: три воркера отпали по
`pod anti-affinity rules` (на каждом уже сидит реплика), три control-plane — по тейнту.
Кандидатов не осталось.

Хвост про вытеснение здесь **не такой**, как в демо 1. Там было `6 Preemption is not helpful`
на все шесть узлов, здесь — `3 No preemption victims found for incoming pod, 3 Preemption is
not helpful`. Разделение ровно по тем же двум группам: для трёх узлов с тейнтом вытеснение
бесполезно в принципе (выселяй хоть весь узел — тейнт останется), а для трёх воркеров
планировщик всерьёз искал, кого выселить, чтобы правило выполнилось, и подходящей жертвы не
нашёл. Разница в формулировках — не косметика: она показывает, что anti-affinity, в отличие
от тейнта, теоретически лечится освобождением домена.

Практический вывод: `required` anti-affinity по `hostname` жёстко ограничивает число реплик
числом узлов. Три воркера — максимум три реплики, четвёртая висит `Pending` бессрочно, до
появления нового узла. Отдельно стоит держать в голове (стендом это **не измерено**), что под
такое же ограничение попадает и обычное обновление Deployment: стратегия `RollingUpdate` по
умолчанию поднимает лишний под сверх `replicas`, а поднимать его будет некуда.

После съёмки реплики возвращены к трём.

### Topology spread: ожидание не сбылось

Deployment `topo-demo`, **четыре** реплики, `maxSkew: 1` по `kubernetes.io/hostname`,
`whenUnsatisfiable: DoNotSchedule`. Ожидалось распределение 2+1+1 и четыре `Running` —
`maxSkew: 1` это должен допускать. Фактически:

```
topo-demo-55b79976d7-6fm49   Running   k8s-volga-wk02
topo-demo-55b79976d7-9kqrj   Running   k8s-volga-wk03
topo-demo-55b79976d7-dpx4d   Pending   <none>
topo-demo-55b79976d7-srk9l   Running   k8s-volga-wk01
```

Четвёртая реплика **тоже осталась `Pending`** — ровно как у жёсткого anti-affinity.
Дословный отказ:

```
  Warning  FailedScheduling  2m37s  default-scheduler  0/6 nodes are available: 3 node(s) didn't match pod topology spread constraints, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 3 No preemption victims found for incoming pod, 3 Preemption is not helpful for scheduling.
```

### Почему: домены считаются по всем шести узлам, тейнты при этом игнорируются

Перекос (`skew`) считается как «число подов в домене минус минимум по доменам». Доменов
`kubernetes.io/hostname` в кластере **шесть**, а не три — метка `hostname` есть у всех узлов,
включая control-plane:

```
k8s-volga-cp01   k8s-volga-cp01   node-role.kubernetes.io/control-plane
k8s-volga-cp02   k8s-volga-cp02   node-role.kubernetes.io/control-plane
k8s-volga-cp03   k8s-volga-cp03   node-role.kubernetes.io/control-plane
k8s-volga-wk01   k8s-volga-wk01   <none>
k8s-volga-wk02   k8s-volga-wk02   <none>
k8s-volga-wk03   k8s-volga-wk03   <none>
```

Попадут ли тейнтованные узлы в расчёт — решает поле `nodeTaintsPolicy`. В объекте пода его
**нет**: API-сервер значение по умолчанию не дописывает, объект хранится ровно таким, каким
его подали:

```json
[{"labelSelector":{"matchLabels":{"app":"topo-demo"}},"maxSkew":1,"topologyKey":"kubernetes.io/hostname","whenUnsatisfiable":"DoNotSchedule"}]
```

Умолчание для `nodeTaintsPolicy` — `Ignore`, и это не догадка: его документирует сам API
кластера. `kubectl explain pod.spec.topologySpreadConstraints.nodeTaintsPolicy` на k8s-volga
отвечает дословно:

```
- Honor: nodes without taints, along with tainted nodes for which the incoming pod
  has a toleration, are included. - Ignore: node taints are ignored. All nodes are included.

If this value is nil, the behavior is equivalent to the Ignore policy.
```

Отсюда всё и следует. Три control-plane узла входят в расчёт как домены с нулём подов,
минимум по доменам равен **0**, и воркер с двумя подами даёт перекос `2 − 0 = 2` при
`maxSkew: 1`. Домен учитывается, а разместиться в нём нельзя: сам под на control-plane не
пустит фильтр тейнтов. Правило оказалось неудовлетворимым, и `DoNotSchedule` честно оставил
под в `Pending`.

Событие это и подтверждает: три воркера отпали по `pod topology spread constraints` (на них
перекос был бы 2), три control-plane — по тейнту.

### Проверка гипотезы: одно поле всё меняет

Тот же Deployment с единственным добавленным полем `nodeTaintsPolicy: Honor`
(`manifests/41-topology-spread-honor.yaml`, объект `topo-honor-demo`):

```
topo-honor-demo-84f74c99b5-6zmxw   Running   k8s-volga-wk02
topo-honor-demo-84f74c99b5-qhw6m   Running   k8s-volga-wk03
topo-honor-demo-84f74c99b5-s7xcs   Running   k8s-volga-wk01
topo-honor-demo-84f74c99b5-t5h6g   Running   k8s-volga-wk02
```

```
      1 k8s-volga-wk01
      2 k8s-volga-wk02
      1 k8s-volga-wk03
```

Все четыре `Running`, распределение — те самые **2+1+1**. `Honor` исключает из расчёта узлы,
тейнты которых под не терпит: доменов остаётся три, минимум становится `1`, перекос воркера с
двумя подами равен `1` и укладывается в `maxSkew`. Гипотеза подтверждена экспериментом, а не
рассуждением: отличие в **поведении** задаёт единственное поле `nodeTaintsPolicy`. Имя
Deployment'а, значение лейбла `demo`, селектор и `labelSelector` ограничения переименованы с
`topo-demo` на `topo-honor-demo`, чтобы два Deployment'а не подхватывали поды друг друга;
`diff -u` этих манифестов показывает именно эти переименования и `nodeTaintsPolicy`.
Остальное — реплики, `maxSkew`, `topologyKey`, `whenUnsatisfiable`, образ и ресурсы —
совпадает.

Оговорка про версии: `nodeTaintsPolicy` и парное ему `nodeAffinityPolicy` появились не сразу,
и на кластерах старых версий поля может не быть вовсе — тогда `Ignore` является единственным
поведением. Проверять надо не по памяти, а на своём кластере:
`kubectl explain pod.spec.topologySpreadConstraints.nodeTaintsPolicy` — если поле есть, оно
описано так же, как в блоке выше. Стенд измерял поведение на k8s 1.36.2; на каких именно
версиях поле стало доступно и стабильным, стендом не проверялось.

### Контраст: anti-affinity запрещает соседство, spread управляет равномерностью

Это главное различие демо. Строки с пометкой «замер» взяты из фикстуры дословно, строки с
пометкой «вывод» стендом не измерялись и следуют из правила:

| | `spread-demo` (anti-affinity) | `topo-honor-demo` (spread, `Honor`) |
|---|---|---|
| правило | не больше **одного** пода в домене | разница между доменами не больше **1** |
| 4 реплики на 3 воркера (замер) | 1+1+1, четвёртая `Pending` | **2+1+1, все четыре `Running`** |
| 3 реплики на 3 воркера (вывод) | 1+1+1, все `Running` — это и замерено выше | 1+1+1: при `Honor` минимум по доменам равен 1, второй под на узел дал бы перекос 2 |
| потолок реплик (вывод) | равен числу узлов | потолка нет: перекос ограничен, а не соседство |

Оговорка по строкам «вывод»: `topo-honor-demo` запускался **только** с четырьмя репликами
(`fixtures/20-placement.txt`), с тремя его не замеряли; поведение при других числах реплик
выведено из правила, а не снято с кластера.

Anti-affinity отвечает на вопрос «можно ли этим подам жить рядом» и отвечает «нет» — жёстко и
без градаций. Topology spread отвечает на другой вопрос: «насколько неровно они лежат» — и
допускает соседство, пока перекос в пределах `maxSkew`. Первое — про изоляцию, второе — про
равномерность; путать их дорого, потому что `required` anti-affinity молча упирает число
реплик в число узлов.

У spread есть и мягкая форма — `whenUnsatisfiable: ScheduleAnyway`: перекос учитывается на
этапе скоринга как предпочтение, но не блокирует размещение. В демо взят жёсткий
`DoNotSchedule`, чтобы результат был однозначным; `ScheduleAnyway` стендом не замерялся.

### Ограничение: по зонам этого показать нельзя

```
k8s-volga-cp01   <none>   <none>
k8s-volga-cp02   <none>   <none>
k8s-volga-cp03   <none>   <none>
k8s-volga-wk01   <none>   <none>
k8s-volga-wk02   <none>   <none>
k8s-volga-wk03   <none>   <none>
```

`topology.kubernetes.io/zone` и `topology.kubernetes.io/region` пусты у **всех шести** узлов
(то же самое видно в `fixtures/00-scheduler.txt`, блок топологических лейблов). Это
ограничение **этого кластера**, а не Kubernetes: механизм spread одинаково работает с любым
ключом.

Разница между `hostname` и `zone` не в синтаксисе, а в смысле. Spread по `hostname` защищает
от падения **узла**; spread по `zone` — от падения **зоны** целиком, то есть стойки, ЦОДа или
AZ у облачного провайдера. На кластере с зонами обычно ставят два ограничения сразу — по
зонам с `maxSkew: 1` и по хостам мягким `ScheduleAnyway`. Воспроизвести это на k8s-volga
нельзя, и ни одно число в этом разделе к зонам не относится.

Отдельно стоит заметить, что и разобранная выше ловушка с `nodeTaintsPolicy: Ignore` на
кластере с зонами выглядела бы иначе: доменов-зон там немного и все они, как правило,
содержат воркеры, так что «пустой домен, в который нельзя попасть» просто не возникает.
Проблема специфична для `topologyKey: kubernetes.io/hostname` на кластере, где часть узлов
закрыта тейнтом.

### Что стенд оставил после себя

- **Объекты демо удалены все.** `kubectl -n cookbook-k8s get pod -l stand=cookbook-scheduling`
  и тот же запрос по `deploy` отдают `No resources found`. Квота namespace вернулась к
  исходным значениям: `pods 13`, `requests.cpu 950m` — ровно те же числа, что были до демо.
- **Метка на узле оставлена намеренно.** `kubectl get nodes -l
  cookbook.khorost.tech/scheduling=demo -o name` → `node/k8s-volga-wk01`, ровно одна строка:
  помечен один узел из шести. Метка нужна демо 3 и демо 4; снимает её `00-node-label.sh
  unset` в конце демо 4 или `teardown.sh` (шаг 2). Что на остальных пяти узлах этого ключа
  нет, видно и в фикстуре демо 3 (`fixtures/30-pending.txt`, колонка `SCHEDULING_LABEL`:
  `demo` у `wk01`, `<none>` у всех прочих).
- **Тейнты не менялись.** Контрольный снимок после демо — те же три control-plane с
  `NoSchedule` и три воркера с `<none>`. `drain`, `cordon`, `uncordon` не вызывались.
- **Чужие нагрузки не тронуты.** Все поды демо — `pause` с `requests` `10m`/`16Mi`, максимум
  на пике — 12 подов демо одновременно (1 + 3 + 4 + 4), то есть `120m` CPU при квоте `4`.

## Демо 3: вечный Pending

Демо 1 и 2 показывали поды, которые не разместились. Это демо — про Pending, который сам не
рассосётся: правило невыполнимо, ждать бессмысленно. Попутно выясняется неприятное — **текст
события у него совпадает байт в байт** с текстом из демо 1, где правило было совсем другое.

### Постановка: правило, которому не отвечает ни один узел

`manifests/50-stuck-pending.yaml` — один под `stuck-affinity` (`pause`, `10m`/`16Mi`) с
жёстким `nodeAffinity` по ключу `cookbook.khorost.tech/scheduling` со значением **`absent`**.
Ключ в кластере существует, значение — нет:

```
NODE             SCHEDULING_LABEL   TAINTS
k8s-volga-cp01   <none>             node-role.kubernetes.io/control-plane
k8s-volga-cp02   <none>             node-role.kubernetes.io/control-plane
k8s-volga-cp03   <none>             node-role.kubernetes.io/control-plane
k8s-volga-wk01   demo               <none>
k8s-volga-wk02   <none>             <none>
k8s-volga-wk03   <none>             <none>
```

Метка `demo` стоит на одном воркере, значения `absent` нет нигде и появиться ему неоткуда.
Под будет висеть, пока его не удалят: планировщик не ставит таймаут и не сдаётся — он
возвращается к такому поду при каждом изменении кластера, каждый раз с тем же результатом.

### Точный текст отказа

```
Events:
  Type     Reason            Age   From               Message
  ----     ------            ----  ----               -------
  Warning  FailedScheduling  47s   default-scheduler  0/6 nodes are available: 3 node(s) didn't match Pod's node affinity/selector, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 6 Preemption is not helpful for scheduling.
```

### Тот же текст, что в демо 1, — при совсем другой причине

Сравните с событием пода `no-toleration` из демо 1. После нормализации возраста (`47s` против
`58s`) строки совпадают **байт в байт** — проверка лежит в конце `fixtures/30-pending.txt`:

```
# cmd: grep -h FailedScheduling fixtures/10-taint.txt fixtures/30-pending.txt | grep -v '^#' | sed 's/[0-9][0-9]*s /<age> /' | sort -u
  Warning  FailedScheduling  <age>   default-scheduler  0/6 nodes are available: 3 node(s) didn't match Pod's node affinity/selector, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 6 Preemption is not helpful for scheduling.
```

А поды при этом устроены совершенно по-разному:

| | демо 1, `no-toleration` | демо 3, `stuck-affinity` |
|---|---|---|
| правило | `nodeSelector: kubernetes.io/hostname=k8s-volga-cp02` | `nodeAffinity` по `…/scheduling In [absent]` |
| сколько узлов не проходят по этому правилу | 5 (три воркера + `cp01` + `cp03`) | **6, то есть все** |
| что мешает на «своём» узле | тейнт control-plane | ничего — подходящего узла просто нет |
| лечится ли добавлением толерации | да | нет |

Разбивка «3 + 3» в обоих случаях означает **разное**. В демо 1 три воркера действительно
отсеялись по селектору, а `cp01`/`cp03` провалили обе проверки и были записаны в корзину
тейнта — на этом и построен вывод про порядок плагинов. В демо 3 по affinity не проходят
**все шесть** узлов, но три control-plane снова ушли в корзину тейнта, потому что
`TaintToleration` отработал раньше. Счётчик «3» рядом с `node affinity/selector` здесь — не
число узлов, которым правило не подошло, а число узлов, у которых это оказалось **первой**
сработавшей причиной.

Практический вывод двойной. Первое: цифры в `FailedScheduling` показывают, какой плагин отсеял
узел **первым**, и не годятся как оценка «скольким узлам правило не подошло». Второе: сам текст
события не называет правило — два разных невыполнимых правила дали одну и ту же строку, так что
по ней нельзя понять, какое именно требование пода не выполнено. Отвечает на это только спека
пода: здесь видно сразу, что значения `absent` нет ни на одном узле и поставить его некому.

Оговорка: «одинаковый текст» — свойство не всех Pending, а именно этих двух **правил**
размещения. `nodeSelector` из демо 1 и `nodeAffinity` из демо 3 обрабатывает один и тот же
фильтр планировщика, и в сообщении он один — `didn't match Pod's node affinity/selector`.
Нехватка ресурса называется отдельным слагаемым: в демо 4 у пода `victims-…-t8nbq` строка
начинается с `0/6 nodes are available: 1 Insufficient ephemeral-storage, …`
(`fixtures/40-preemption.txt`) — совпадения с текстом выше нет.

Хвост `preemption: 0/6 … 6 Preemption is not helpful for scheduling.` читается буквально:
планировщик проверил, спасёт ли выселение чужих подов, и ответил «нет» по всем шести узлам.
Освобождение места правило не чинит. В демо 4 этот же хвост выглядит иначе — там вытеснение
как раз помогает.

## Демо 4: вытеснение

Обратная ситуация: место кончилось, а пришедший под важнее уже запущенных. Планировщик тогда
не оставляет его в очереди, а **выселяет** менее приоритетные поды, освобождая место.

### Почему демо идёт по `ephemeral-storage`, а не по CPU

Честное ограничение стенда, а не свойство Kubernetes. Вытеснение включается, когда
вытесняющий под **не помещается**: `жертвы + вытесняющий > свободно на узле`. Значит стенду
надо суметь занять узел почти целиком. По CPU это арифметически недостижимо — мешает квота
самого стенда (числа из разведки, `fixtures/40-preemption.txt`):

| | лимит | использовано | доступно стенду |
|---|---|---|---|
| квота `cookbook-quota`, `requests.cpu` | `4` | `950m` | **3050m** |
| узел `k8s-volga-wk01`, CPU | `allocatable 3950m` | запрошено `780m` | **свободно 3170m** |

Разрыв в **120m** и решает дело: занять узел под завязку квота не даёт, а значит и «не
поместиться» нашему поду не даст. По памяти разрыв ещё больше — `3104Mi` доступных против
`~6405Mi` свободных на узле. Квота так и задумана: не позволить одному стенду забрать общий
узел. Ломать её ради демонстрации нельзя.

Ось `ephemeral-storage` от этого ограничения свободна, и это проверено перед демо, а не
принято на веру:

- **квота её не ограничивает вовсе** — в `hard` только
  `{"limits.cpu":"8","limits.memory":"8Gi","pods":"30","requests.cpu":"4","requests.memory":"4Gi"}`;
- **`LimitRange cookbook-limits` её не режет** — `max` задан только для `cpu` и `memory`;
- **на узле её никто не просит** — `ephemeral-storage 0 (0%)` из `55573269617` байт
  (**51,75 GiB**), и поимённый обход всех 20 чужих подов на `wk01` даёт пустое значение
  `eph=` у каждого.

Поды демо — `pause`, они на диск не пишут ничего: резервируется только планировочный бюджет,
реального расхода диска нет. Механика вытеснения от выбора оси не зависит — планировщик
одинаково считает любой скалярный ресурс.

### Как демо сделано безопасным

Кластер общий и живой: на `wk01` крутится 20 чужих подов, включая `coredns`, `cilium` и
`vector`. Выселение здесь — операция, которую нельзя ставить «на авось». Страховок четыре, но
они не равноправны: **первые две независимо друг от друга достаточны**, чтобы чужой под не был
вытеснен, а третья и четвёртая вытеснение не предотвращают вовсе — они ограничивают радиус
демо и его побочные эффекты.

1. **Приоритет жертв отрицательный.** `manifests/60-priority-classes.yaml` заводит
   `cookbook-low` = **−10** и `cookbook-high` = **1000**. Поды без `priorityClassName` имеют
   приоритет **0** — то есть **выше** наших жертв. Планировщик выселяет всех, кто ниже
   вытесняющего, а затем возвращает обратно по убыванию приоритета; наши `−10` пробуются
   последними и в жертвах остаются именно они.
2. **Ось выбрана так, что выселение чужого пода ничего бы не дало.** Все чужие поды на узле
   просят `ephemeral-storage 0` — удаление любого из них освобождает ноль байт и не
   приближает `preemptor` к размещению. Бесполезного кандидата планировщик из набора жертв
   отбрасывает. Отсюда следует, что чужое осталось бы на месте и без первой страховки, — но это
   вывод из замера (`eph=` пусто у всех 20 чужих подов узла), а не отдельный опыт: чтобы снять
   его, пришлось бы отключить первую страховку, чего мы не делали.
3. **`globalDefault: false` у обоих классов** — страховка не от вытеснения, а от того, чтобы
   стенд не переписал приоритеты всему кластеру. `PriorityClass` — объект кластерный, и
   `globalDefault: true` назначил бы класс всем подам кластера, которые не указали
   `priorityClassName` явно. Контроль в фикстуре: `kubectl get priorityclass -o json | grep -c
   '"globalDefault": true'` → **0**, ни одного класса по умолчанию в кластере нет.
4. **Все участники приколоты к одному узлу** через `nodeSelector` по метке, которую стенд сам
   же и поставил. Тейнты не ставились, `drain`/`cordon`/`uncordon` не вызывались. Это тоже
   ограничение радиуса, а не защита: чужие поды **на самом `wk01`** от вытеснения защищены
   страховками 1 и 2, а не этой — она лишь гарантирует, что остальные пять узлов демо не
   касается вообще.

Значение `1000` у вытесняющего выбрано заведомо ниже системных классов
(`system-cluster-critical` = `2000000000`, `system-node-critical` = `2000001000`), так что
системные поды узла нашему демо неподсудны в принципе.

### Расчёт и постановка

`manifests/61-preemption.yaml` — Deployment `victims` на 4 реплики по `ephemeral-storage: 12Gi`
и Pod `preemptor` на `8Gi`:

```
4 × 12Gi = 48Gi                       помещаются в 51,75 GiB, остаётся ≈ 3,75 GiB
preemptor 8Gi > 3,75 GiB              без вытеснения не помещается
выселение ОДНОЙ жертвы даёт +12Gi     15,75 GiB ≥ 8Gi, хватает
```

**Манифест применяется в два приёма, и это принципиально.** Очередь планировщика упорядочена
по приоритету: при одновременном создании `preemptor` с его `1000` был бы размещён первым, на
свободный узел, и вытеснять стало бы некого. Сначала место занимают жертвы:

```bash
sed '/^# ==== ДОКУМЕНТ 2/,$d' manifests/61-preemption.yaml | kubectl apply -f -
kubectl -n cookbook-k8s rollout status deploy/victims --timeout=120s
sed -n '/^# ==== ДОКУМЕНТ 2/,$p' manifests/61-preemption.yaml | kubectl apply -f -
```

После четырёх жертв узел показывает `ephemeral-storage 48Gi (92%)` — при том что CPU занят
всего на `820m (20%)`, а память на `1094Mi (14%)`. Узел «полон» ровно по той оси, по которой
мы его наполнили.

### Хроника: 5 секунд от создания до Running

```
11:41:07 создаём preemptor
11:41:07 preemptor 0/1 ContainerCreating 0s; victims-…-bsj4p 1/1 Running 53s; victims-…-mlxlt 1/1 Running 53s; victims-…-n4jtb 1/1 Running 53s; victims-…-t8nbq 0/1 Pending 0s; victims-…-vknnc 0/1 Completed 53s;
11:41:12 preemptor 1/1 Running 5s; victims-…-bsj4p 1/1 Running 58s; victims-…-mlxlt 1/1 Running 58s; victims-…-n4jtb 1/1 Running 58s; victims-…-t8nbq 0/1 Pending 5s;
11:42:04 preemptor 1/1 Running 57s; victims-…-bsj4p 1/1 Running 110s; victims-…-mlxlt 1/1 Running 110s; victims-…-n4jtb 1/1 Running 110s; victims-…-t8nbq 0/1 Pending 57s;
```

Всё произошло внутри первой же секунды: жертва `vknnc` уже `Completed`, замена `t8nbq` уже
создана и уже `Pending`, `preemptor` уже создаёт контейнер. Ещё через пять секунд он `Running`.
Дальше картина не менялась до конца минутного наблюдения.

### Кто кого вытеснил

```
# cmd: kubectl get events -A --field-selector reason=Preempted -o custom-columns=NS:.metadata.namespace,POD:.involvedObject.name,MSG:.message --no-headers
cookbook-k8s   victims-5d689d946d-vknnc   Preempted by pod 9b43a2d1-591a-4ebe-80b8-1cc6b618fa89 on node k8s-volga-wk01
```

Две детали текста стоит прочесть буквально:

- **Вытеснитель назван по UID, а не по имени.** На k8s 1.36.2 сообщение `Preempted by pod
  <uid> on node <node>` не содержит ни имени, ни namespace виновника — чтобы понять, кто
  выселил под, придётся сопоставлять UID вручную. Здесь сопоставление в фикстуре:
  `kubectl -n cookbook-k8s get pod preemptor -o jsonpath='{.metadata.uid}'` →
  `9b43a2d1-591a-4ebe-80b8-1cc6b618fa89`, тот же самый.
- **Событие ровно одно.** `kubectl get events -A --field-selector reason=Preempted | wc -l` →
  **1** по всему кластеру. Планировщик выселил **минимально необходимое** число подов: одной
  жертвы (`12Gi`) хватило, чтобы `8Gi` поместились, и трогать вторую он не стал. Три остальные
  жертвы так и остались `Running`.

Сам `preemptor` при этом получил **два** события подряд:

```
Warning  FailedScheduling  pod/preemptor  0/6 nodes are available: 1 Insufficient ephemeral-storage, 2 node(s) didn't match Pod's node affinity/selector, 3 node(s) had untolerated taint(s).
Normal   Scheduled         pod/preemptor  Successfully assigned cookbook-k8s/preemptor to k8s-volga-wk01
```

Сначала обычная фильтрация не нашла узла — и здесь снова видна разбивка по первой причине:
`1` (это `wk01`, где не хватило места) + `2` (воркеры, не подошедшие по `nodeSelector`) + `3`
(control-plane с тейнтом) = 6. И обратите внимание, чего в этом `FailedScheduling` **нет**:
хвоста `preemption: …`. Во всех предыдущих демо он был и объяснял, почему выселение не
поможет. Здесь оно помогло — планировщик наметил узел, выселил жертву и на следующем цикле
разместил под.

### Что стало с вытесненной жертвой

```
NAME                       PHASE     NODE             PRIO   EPH
preemptor                  Running   k8s-volga-wk01   1000   8Gi
victims-5d689d946d-bsj4p   Running   k8s-volga-wk01   -10    12Gi
victims-5d689d946d-mlxlt   Running   k8s-volga-wk01   -10    12Gi
victims-5d689d946d-n4jtb   Running   k8s-volga-wk01   -10    12Gi
victims-5d689d946d-t8nbq   Pending   <none>           -10    12Gi
```

Вытеснение **не уменьшает** `replicas`. Deployment немедленно создал замену `t8nbq` взамен
выселенной `vknnc` — и та встала в очередь на тот же узел, где места по-прежнему нет:

```
Warning  FailedScheduling  pod/victims-5d689d946d-t8nbq  0/6 nodes are available: 1 Insufficient ephemeral-storage, 2 node(s) didn't match Pod's node affinity/selector, 3 node(s) had untolerated taint(s). no new claims to deallocate, preemption: 0/6 nodes are available: 1 No preemption victims found for incoming pod, 5 Preemption is not helpful for scheduling.
```

`No preemption victims found for incoming pod` — у замены приоритет `−10`, и никого ниже себя
в кластере она не находит. Выселить, чтобы освободить место себе, ей некого.

Отсюда практический вывод, который стоит держать в голове при раздаче отрицательных
приоритетов: контроллер будет пересоздавать выселенную реплику бесконечно, а планировщик —
бесконечно отказывать ей в размещении. Вытеснение освободило место **однократно**; нагрузка,
которую выселили, никуда не делась и продолжает стучаться в очередь. Узел после вытеснения:
`ephemeral-storage 44Gi (85%)` — это `3 × 12Gi` жертв плюс `8Gi` вытеснившего.

### Доказательство, что вытеснены только свои поды

Главная проверка демо. Срез всех подов `wk01` снимался **до** создания жертв и **после**
вытеснения, с временами старта; поды стенда из сравнения исключены:

```
# cmd: diff <(срез ДО) <(срез ПОСЛЕ без подов стенда)
(diff пуст: все 20 чужих подов на месте, с теми же временами старта — ни один не перезапускался)
```

Отдельного счётчика не нужно: до демо на узле было 20 чужих подов
(`fixtures/40-preemption.txt`, блок разведки), после — те же 20 поимённо
(`fixtures/40-preemption.txt`, блок 4), и `diff` между срезами пуст.

Дополнительно — счётчик перезапусков по каждому из 20 чужих подов узла: везде **0**, включая
`kube-system/coredns`, `kube-system/cilium`, `logging/vector` и `cookbook-net/server`. Плюс
единственное событие `Preempted` во всём кластере, разобранное выше, называет наш собственный
под. Совпадение времён старта важнее счётчика: перезапущенный или пересозданный под получил
бы новое `startTime`, а `diff` пуст.

### Что стенд оставил после себя

Демо 4 — единственное в стенде, которое создаёт **кластерные** объекты, поэтому уборка
проверена отдельно и по всем трём следам:

- **Подов стенда нет.** `kubectl -n cookbook-k8s get pod -l stand=cookbook-scheduling` →
  `No resources found`.
- **Оба `PriorityClass` удалены.** `kubectl get priorityclass` отдаёт только системные
  `system-cluster-critical` и `system-node-critical`.
- **Метка с узла снята.** `kubectl get nodes -l cookbook.khorost.tech/scheduling -o name` —
  пустой вывод. Это последнее демо стенда, метка больше никому не нужна.
- **Квота вернулась к исходной.** `pods 13`, `requests.cpu 950m`, `requests.memory 992Mi` —
  ровно те числа, что были до демо 1.
- **Ось освобождена полностью.** На `wk01` снова `ephemeral-storage 0 (0%)`.
- **Стенды прошлых фаз целы.** `trace-demo` — два пода `Running`, аптайм не сбрасывался.

## Ограничения и особенности

Здесь — всё, что важно не упустить при чтении демо выше, в одном месте:

1. **Зон в кластере нет — spread показан только по хостам.** `topology.kubernetes.io/zone`
   пуст у всех **шести** узлов, как и `topology.kubernetes.io/region`
   (`fixtures/20-placement.txt`, блок про зоны); больше того, в объектах узлов нет вообще ни
   одного ключа со словами `topology`, `zone` или `region` — проверка в
   `fixtures/00-scheduler.txt` даёт `0` совпадений. Поэтому topology spread в демо 2 построен
   только по `kubernetes.io/hostname`. Это ограничение **кластера**, а не Kubernetes:
   механизм одинаково работает с любым ключом. Практический смысл при этом разный — spread по
   `hostname` защищает от падения узла, spread по `zone` от падения стойки, ЦОДа или AZ, и на
   кластере с зонами обычно ставят оба ограничения сразу (по зонам жёстко, по хостам мягким
   `ScheduleAnyway`). Ни одно число этого стенда к зонам не относится.
2. **Умолчание `nodeTaintsPolicy` — `Ignore`, и оно объясняет «неожиданный» `Pending`.**
   Поля в объекте пода нет вовсе (`fixtures/20-placement.txt`, вывод
   `.spec.topologySpreadConstraints`), а `kubectl explain` того же кластера документирует:
   `If this value is nil, the behavior is equivalent to the Ignore policy.` Домены при этом
   считаются по **всем** шести узлам, включая тейнтованные: минимум по доменам равен `0`,
   воркер с двумя подами даёт перекос `2` при `maxSkew: 1`, и четвёртая реплика `topo-demo`
   остаётся `Pending` — `1+1+1+Pending` вместо ожидаемых `2+1+1`. Это не рассуждение, а
   контрольный эксперимент: тот же Deployment с единственным добавленным
   `nodeTaintsPolicy: Honor` (`manifests/41-topology-spread-honor.yaml`) даёт все четыре
   `Running` и распределение `2+1+1`. На кластере с зонами ловушка выглядела бы иначе:
   доменов-зон немного и все они обычно содержат воркеры, «пустого домена, в который нельзя
   попасть» просто не возникает.
3. **Вытеснение показано ограниченно и намеренно.** Конфигурация демо 4 — не «типичная», а
   безопасная для общего кластера: жертвы несут **отрицательный** приоритет (`cookbook-low` =
   `−10`, ниже подов без класса, у которых `0`), все участники приколоты `nodeSelector`'ом к
   одному воркеру, оба `PriorityClass` объявлены без `globalDefault`, а значение вытесняющего
   (`1000`) заведомо ниже системных классов (`2000000000` и `2000001000`). В проде приоритеты
   обычно раздают иначе — положительными значениями по классам нагрузки; здесь же задача была
   показать механику, ничего не задев. Механика вытеснения от этого не меняется, но переносить
   **числа и расстановку** демо на боевую конфигурацию нельзя.
4. **Демо вытеснения идёт по `ephemeral-storage`, а не по CPU — из-за арифметики квоты.**
   Стенду доступно `3050m` CPU (квота `requests.cpu` `4` минус занятые `950m`), а свободно на
   целевом узле `3170m` (`allocatable 3950m` минус запрошенные `780m`). Разрыв `120m` и решает
   дело: занять узел под завязку квота не даёт, значит и «не поместиться» вытесняющему поду не
   даст — вытеснение по CPU на этом кластере недостижимо. По памяти разрыв ещё больше:
   `3104Mi` доступных против `≈6405Mi` свободных на узле. Ось `ephemeral-storage` от этого
   свободна (квота её не ограничивает, `LimitRange` тоже, и на узле её никто не просит), и это
   честное ограничение стенда, а не свойство Kubernetes: планировщик одинаково считает любой
   скалярный ресурс.
5. **Тексты отказа двух разных правил совпадают байт в байт, а счётчики означают не то, что
   кажется.** Событие `FailedScheduling` у `no-toleration` (демо 1, жёсткий `nodeSelector` на
   узел под тейнтом) и у `stuck-affinity` (демо 3, невыполнимое `nodeAffinity`) после
   нормализации возраста — одна и та же строка; сверка лежит в конце
   `fixtures/30-pending.txt`. Причина в том, что оба правила обрабатывает один фильтр
   планировщика, и в сообщении он один — `didn't match Pod's node affinity/selector`. Отсюда
   второе: **числа в разбивке — это «первая сработавшая причина», а не «сколько узлов правилу
   не подошли»**. В демо 3 по affinity не проходят все шесть узлов, но три control-plane
   посчитаны в корзине тейнта, потому что `TaintToleration` отработал раньше. По самому тексту
   события понять, какое правило нарушено, нельзя — отвечает только спека пода.
6. **Событие `Preempted` называет вытеснителя по UID, а не по имени.** На k8s 1.36.2 сообщение
   выглядит как `Preempted by pod 9b43a2d1-591a-4ebe-80b8-1cc6b618fa89 on node k8s-volga-wk01`
   — ни имени, ни namespace виновника в нём нет. Чтобы понять, кто выселил под, UID приходится
   сопоставлять вручную (в фикстуре это сделано:
   `kubectl get pod preemptor -o jsonpath='{.metadata.uid}'` даёт тот же UID).
7. **`kubectl top` дрейфует, и не всё в нём сопоставимо.** Значение — скользящее среднее за
   последние секунды, поэтому по `wk02` в одной и той же фикстуре встречаются `68m` и `72m`:
   это дрейф живой нагрузки между двумя вызовами, а не расхождение методик. Ссылаться в тексте
   на «то самое число» нельзя — при повторе замера оно будет другим. Отдельно:
   `describe node → Allocated resources` и `kubectl top node` **сравнивать между собой
   нельзя**, это разные области измерения — первое суммирует `requests` подов, второе
   показывает working set узла целиком (поды плюс kubelet, containerd, Talos, сетевой стек
   ядра и page cache). С `requests` сопоставима только сумма `kubectl top pod` по подам узла.
   `requests` при этом статичны: во втором замере `describe node` вернул по всем трём воркерам
   те же значения, что и в первом.
8. **`61-preemption.yaml` нельзя применять одним `kubectl apply -f`.** Очередь планировщика
   упорядочена по приоритету: при одновременном создании `preemptor` с его `1000` был бы
   размещён **первым**, на ещё свободный узел, и вытеснять оказалось бы некого — демо просто
   не состоялось бы. Порядок обязателен: сначала документ 1 (четыре жертвы) и ожидание, пока
   все четыре станут `Running`, и только потом документ 2. Команды разделения по документам —
   в шапке самого манифеста и в «Порядке запуска» ниже. Оговорка: «одним `apply` вытеснения не
   вышло бы» — вывод из правила упорядочивания очереди, а **не** замер: одновременного
   применения стенд не делал, двухтактный порядок использовался с первого прогона.

## Порядок запуска

Демо идут **строго по порядку**: демо 2 ставит метку на узел, а демо 3 и 4 на неё опираются;
демо 4, кроме того, требует предварительной разведки узла. Блоки внутри одного демо тоже
последовательны — где порядок принципиален, об этом сказано в комментарии.

Про сборку фикстур: первый блок каждой из фикстур `00`–`30` снимает `capture.sh` (он
**перезаписывает** файл через `tee`, поэтому вызывается один раз и первым), остальные блоки
дописываются к тому же файлу (`>> fixtures/<имя>.txt`); дословная команда каждого блока стоит
рядом с ним в строке `# cmd:`. `fixtures/40-preemption.txt` собран целиком дописыванием, без
`capture.sh` — потому у него и нет шапки `# captured on …`, которая есть у остальных четырёх.

```bash
export KUBECONFIG="…/k8s-volga/kubeconfig"
kubectl config current-context   # обязан быть admin@k8s-volga

# демо 0 — только чтение, ничего не создаёт
bash capture.sh 00-scheduler -- get nodes \
  -o custom-columns=NAME:.metadata.name,TAINTS:.spec.taints,CPU:.status.allocatable.cpu,MEM:.status.allocatable.memory
# остальные блоки фикстуры дописываются к тому же файлу (см. `# cmd:` внутри неё)

# сумма top pod по узлу — величина, сопоставимая с requests
# (metrics API не принимает --field-selector spec.nodeName, поэтому джойн двух API)
N=k8s-volga-wk02
join -j 1 \
  <(kubectl get pods -A --field-selector spec.nodeName=$N \
      -o custom-columns=NS:.metadata.namespace,N:.metadata.name --no-headers \
    | awk '{print $1"/"$2}' | sort) \
  <(kubectl top pod -A --no-headers | awk '{print $1"/"$2, $3, $4}' | sort) \
| awk '{c+=$2+0; m+=$3+0; n++} END{print "pods="n, "sum_cpu="c"m", "sum_mem="m"Mi"}'

# демо 1 — тейнт как первый фильтр.
# ВНИМАНИЕ: под with-toleration садится на боевой control-plane узел.
# Он крошечный (pause, 10m/16Mi) и удаляется последней командой блока — не пропускать её.
bash apply.sh manifests/10-taint-toleration.yaml
sleep 45
bash capture.sh 10-taint -- get pod -n cookbook-k8s no-toleration with-toleration \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName,SCHEDULED:.status.conditions[0].type
kubectl -n cookbook-k8s describe pod no-toleration   | awk '/^Events:/{f=1} f'
kubectl -n cookbook-k8s describe pod with-toleration | awk '/^Events:/{f=1} f'
kubectl -n cookbook-k8s get pod with-toleration -o jsonpath='{range .spec.tolerations[*]}{.key}{" op="}{.operator}{" effect="}{.effect}{" tolerationSeconds="}{.tolerationSeconds}{"\n"}{end}'
kubectl -n cookbook-k8s delete pod no-toleration with-toleration --ignore-not-found

# добор к демо 1 — всё только на чтение, поды уже удалены
kubectl get limitrange -n cookbook-k8s \
  -o custom-columns=NAME:.metadata.name,TYPE:.spec.limits[0].type,MAX-CPU:.spec.limits[0].max.cpu,MAX-MEM:.spec.limits[0].max.memory,MIN:.spec.limits[0].min
kubectl -n kube-system get pod cilium-99lgh -o jsonpath='{.spec.tolerations}'
kubectl get pod -A --no-headers \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,SEC:.spec.tolerations[*].tolerationSeconds
# дословный warning PodSecurity: server-side dry-run гоняет admission, но НЕ сохраняет объекты
kubectl apply --dry-run=server -f manifests/10-taint-toleration.yaml
kubectl get pod -n cookbook-k8s -l stand=cookbook-scheduling   # контроль: No resources found

# демо 2 — куда сядет под.
# ВНИМАНИЕ: метка на узле ставится здесь и НЕ снимается в конце демо — её используют демо 3 и 4.
bash manifests/00-node-label.sh set        # печатает выбранный worker
bash capture.sh 20-placement -- get nodes -l cookbook.khorost.tech/scheduling=demo \
  -o 'custom-columns=NAME:.metadata.name,LABEL:.metadata.labels.cookbook\.khorost\.tech/scheduling'

# 2а: node affinity — притянуть под к помеченному узлу
bash apply.sh manifests/20-node-affinity.yaml
kubectl -n cookbook-k8s wait --for=condition=Ready pod/affinity-demo --timeout=90s
kubectl -n cookbook-k8s get pod affinity-demo \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName --no-headers

# 2б: pod anti-affinity — развести реплики по узлам
bash apply.sh manifests/30-pod-antiaffinity.yaml
kubectl -n cookbook-k8s rollout status deploy/spread-demo --timeout=90s
kubectl -n cookbook-k8s get pod -l app=spread-demo \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName --no-headers
# цена жёсткого правила: воркеров три, четвёртой реплике места нет — она зависает Pending
kubectl -n cookbook-k8s scale deploy spread-demo --replicas=4
sleep 40
P=$(kubectl -n cookbook-k8s get pod -l app=spread-demo \
      --field-selector status.phase=Pending -o jsonpath='{.items[0].metadata.name}')
kubectl -n cookbook-k8s describe pod "$P" | awk '/^Events:/{f=1} f'
kubectl -n cookbook-k8s scale deploy spread-demo --replicas=3

# 2в: topology spread — четвёртая реплика ТОЖЕ остаётся Pending (см. разбор про nodeTaintsPolicy)
bash apply.sh manifests/40-topology-spread.yaml
sleep 45
kubectl -n cookbook-k8s get pod -l app=topo-demo \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName --no-headers
P=$(kubectl -n cookbook-k8s get pod -l app=topo-demo \
      --field-selector status.phase=Pending -o jsonpath='{.items[0].metadata.name}')
kubectl -n cookbook-k8s describe pod "$P" | awk '/^Events:/{f=1} f'
# в объекте пода поля nodeTaintsPolicy нет — действует умолчание Ignore
kubectl -n cookbook-k8s get pod "$P" -o jsonpath='{.spec.topologySpreadConstraints}'
kubectl get nodes --no-headers \
  -o 'custom-columns=NAME:.metadata.name,HOSTNAME:.metadata.labels.kubernetes\.io/hostname,TAINTS:.spec.taints[*].key'

# 2в (проверка гипотезы): то же самое + nodeTaintsPolicy: Honor — все четыре Running, 2+1+1
bash apply.sh manifests/41-topology-spread-honor.yaml
kubectl -n cookbook-k8s rollout status deploy/topo-honor-demo --timeout=120s
kubectl -n cookbook-k8s get pod -l app=topo-honor-demo \
  -o custom-columns=NODE:.spec.nodeName --no-headers | sort | uniq -c

# зон в кластере нет — spread по zone на k8s-volga не воспроизводится
kubectl get nodes --no-headers \
  -o 'custom-columns=NAME:.metadata.name,ZONE:.metadata.labels.topology\.kubernetes\.io/zone,REGION:.metadata.labels.topology\.kubernetes\.io/region'

# уборка демо 2 — МЕТКУ НА УЗЛЕ НЕ СНИМАЕМ, она нужна демо 3 и демо 4
kubectl -n cookbook-k8s delete pod affinity-demo --ignore-not-found
kubectl -n cookbook-k8s delete deploy spread-demo topo-demo topo-honor-demo --ignore-not-found
kubectl -n cookbook-k8s get pod,deploy -l stand=cookbook-scheduling   # контроль: No resources found
kubectl get nodes -l cookbook.khorost.tech/scheduling=demo -o name    # контроль: метка на месте
kubectl describe quota -n cookbook-k8s                                # контроль: pods 13, requests.cpu 950m
```

```bash
# демо 3 — вечный Pending. Под не размещается никогда: удалить сразу после снятия фикстуры.
bash apply.sh manifests/50-stuck-pending.yaml
sleep 40
bash capture.sh 30-pending -- get pod -n cookbook-k8s stuck-affinity \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName --no-headers
kubectl -n cookbook-k8s describe pod stuck-affinity | awk '/^Events:/{f=1} f'
kubectl -n cookbook-k8s delete pod stuck-affinity --ignore-not-found

# демо 4 — вытеснение. ⚠️ Кластер общий: см. раздел «Как демо сделано безопасным».
# шаг 1: разведка. Без неё демо не запускать — размеры считаются от ФАКТА, а не от памяти.
# Целевой узел — тот самый, что помечен в демо 2 (в фикстурах это k8s-volga-wk01);
# имя не хардкодим, а берём по метке — 00-node-label.sh выбирает узел сам.
NODE=$(kubectl get nodes -l cookbook.khorost.tech/scheduling \
         -o jsonpath='{.items[0].metadata.name}')
echo "целевой узел демо 4: $NODE"          # обязан быть непустым, иначе метка не поставлена
kubectl describe node "$NODE" | grep -A10 'Allocated resources'   # ephemeral-storage обязан быть 0
kubectl get node "$NODE" -o jsonpath='{.status.allocatable}{"\n"}'
kubectl get quota -n cookbook-k8s -o jsonpath='{range .items[*]}hard={.status.hard}{"\n"}used={.status.used}{"\n"}{end}'
kubectl describe limitrange cookbook-limits -n cookbook-k8s   # ephemeral-storage не должен фигурировать
# кто на узле уже просит ephemeral-storage: у всех чужих подов значение должно быть пустым —
# иначе расчёт «48Gi из 51,75 GiB» поедет, и размеры жертв надо пересчитывать
kubectl get pod -A --field-selector spec.nodeName="$NODE" \
  -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{" eph="}{.spec.containers[0].resources.requests.ephemeral-storage}{"\n"}{end}'
# срез чужих подов узла ДО демо — база для доказательства безопасности в конце
kubectl get pod -A --field-selector spec.nodeName="$NODE" \
  -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{" started="}{.status.startTime}{"\n"}{end}' \
  | sort > /tmp/wk01-before.txt

# шаг 2: классы приоритета. globalDefault обязан быть false у обоих — проверить перед демо.
bash apply.sh manifests/60-priority-classes.yaml
kubectl get priorityclass -o json | grep -c '"globalDefault": true'   # обязан быть 0

# шаг 3: сперва ТОЛЬКО жертвы, дождаться всех четырёх Running (иначе вытеснять будет некого)
sed '/^# ==== ДОКУМЕНТ 2/,$d' manifests/61-preemption.yaml | kubectl apply -f -
kubectl -n cookbook-k8s rollout status deploy/victims --timeout=120s
kubectl describe node "$NODE" | grep -A10 'Allocated resources'   # ephemeral-storage 48Gi (92%)

# шаг 4: вытесняющий под + хроника
sed -n '/^# ==== ДОКУМЕНТ 2/,$p' manifests/61-preemption.yaml | kubectl apply -f -
for i in $(seq 1 12); do
  printf '%s ' "$(date -u +%T)"
  kubectl -n cookbook-k8s get pod -l stand=cookbook-scheduling --no-headers | tr '\n' ';' ; echo
  sleep 5
done

# шаг 5: кто кого вытеснил + ДОКАЗАТЕЛЬСТВО, что тронуты только свои поды
kubectl -n cookbook-k8s get pod preemptor -o jsonpath='{.metadata.uid}{"\n"}'   # сверить с UID в событии
kubectl get events -A --field-selector reason=Preempted \
  -o custom-columns=NS:.metadata.namespace,POD:.involvedObject.name,MSG:.message --no-headers
kubectl get pod -A --field-selector spec.nodeName="$NODE" \
  -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{" started="}{.status.startTime}{"\n"}{end}' \
  | sort | grep -v -E 'cookbook-k8s/(victims|preemptor)' > /tmp/wk01-after.txt
diff /tmp/wk01-before.txt /tmp/wk01-after.txt   # обязан быть ПУСТ: ни один чужой под не тронут
# и счётчик перезапусков у чужих подов узла — везде 0
kubectl get pod -A --field-selector spec.nodeName="$NODE" \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount \
  --no-headers | grep -v -E 'victims|preemptor'

# уборка демо 4 — полная, включая КЛАСТЕРНЫЕ объекты и метку узла.
# ⚠️ Метку снимаем ПОСЛЕДНЕЙ: пока она стоит, $NODE вычисляется по ней.
kubectl -n cookbook-k8s delete pod preemptor --ignore-not-found
kubectl -n cookbook-k8s delete deploy victims --ignore-not-found
sleep 15
kubectl describe node "$NODE" | grep ephemeral-storage               # контроль: ось освобождена, 0 (0%)
kubectl delete priorityclass cookbook-low cookbook-high --ignore-not-found
bash manifests/00-node-label.sh unset
kubectl get priorityclass --no-headers                               # контроль: только системные
kubectl get nodes -l cookbook.khorost.tech/scheduling -o name        # контроль: пусто
kubectl describe quota -n cookbook-k8s | tail -8                     # контроль: pods 13, requests.cpu 950m
kubectl -n cookbook-k8s get pod -l app=trace-demo --no-headers       # контроль: стенд architecture цел
```

## Teardown

```bash
bash teardown.sh
```

Скрипт проверяет контекст (`admin@k8s-volga`) и отказывается работать в чужом кластере. Затем
убирает следы стенда из **трёх** мест — в отличие от предыдущих стендов серии, которым хватало
одного:

1. **Объекты в namespace `cookbook-k8s`** — строго по метке `stand: cookbook-scheduling`
   (`all`, `cm`, `secret`, `sa`, `role`, `rolebinding`, `pdb`). `pvc` в этом списке
   намеренно нет: **стенд не создаёт ни одного тома** — ни PVC, ни `volumeClaimTemplates`, ни
   вообще `volumes` (поиск по `manifests/` не даёт ни одного совпадения). Все восемь
   YAML-манифестов — это только Pod'ы и Deployment'ы с образом `registry.k8s.io/pause:3.10`
   плюс два `PriorityClass`.
   Демо 4 хоть и работает с `ephemeral-storage`, но это **планировочный бюджет** узла, а не
   том: объектов хранения он не порождает, освобождается вместе с подом, и удалять после него
   нечего.
2. **Метка `cookbook.khorost.tech/scheduling` с узлов.** Узлы ищутся по самой метке
   (`kubectl get nodes -l …`), а не по имени: если метку успели поставить на несколько узлов,
   снимется со всех; если её нет нигде — скрипт скажет, что снимать нечего.
3. **Кластерные `PriorityClass`** `cookbook-low` и `cookbook-high`. Они не лежат в namespace,
   и обычным `delete -n` их не убрать — поэтому отдельный шаг.

Чего `teardown.sh` не делает:

- **Namespace `cookbook-k8s` не удаляет** — он общий для всех стендов серии.
- **Чужие объекты в том же namespace не трогает** — у соседних стендов свои метки
  (`stand: cookbook-secrets`, `stand: cookbook-architecture` и т. д.).
- **Системные метки и тейнты узлов не трогает вовсе** — стенд их только читал.
- **В `kube-system` не заходит.**

Скрипт идемпотентен: `--ignore-not-found` и поиск метки по селектору позволяют запускать его
повторно на уже убранном стенде.
