# Стенд: hardening контейнеров и подов — Pod Security, seccomp, capabilities (k8s-volga)

Живой стенд для серии «Kubernetes на практике», статья 10 (последняя). Про то, чем на самом
деле ограничен контейнер: `securityContext`, Linux capabilities, Pod Security Standards,
seccomp, user namespaces. Стенд построен на **контрасте уровней**: один и тот же под,
отклонённый профилем `restricted`, проходит под `baseline` — и наоборот, — поэтому у стенда
**два** собственных namespace с разными уровнями Pod Security.

Стенд снят целиком: разведка возможностей кластера (демо 0), контейнер по умолчанию против
hardened (демо 1), тексты отказов Pod Security Standards с контрастом `baseline`/`restricted`
(демо 2), seccomp тремя режимами и user namespaces (демо 3). Все числа — вывод команд и
`/proc` на живом k8s-volga (Talos 1.36), ничего по памяти; чего кластер не даёт (AppArmor,
user namespaces), зафиксировано фактом и вынесено в «Ограничения и особенности».

## Назначение

Показать не «контейнер изолирован», а из чего эта изоляция складывается и где её границы:
- `securityContext` пода и контейнера — `runAsNonRoot`, `allowPrivilegeEscalation`,
  `readOnlyRootFilesystem`, `capabilities.drop`;
- Pod Security Standards как admission-контроль: `baseline` против `restricted` на одном и
  том же поде;
- seccomp — какой профиль реально применяется к процессу (по `/proc/1/status`);
- то, чего кластер **не** даёт (AppArmor, user namespaces) — зафиксировано фактом, а не
  предположением, и вынесено в ограничения стенда.

## Требования к кластеру

- Kubernetes 1.36+ с включённым Pod Security admission (штатно с 1.25).
- Права **`create`/`delete`** на Namespace, ResourceQuota, LimitRange и Pod — стенд заводит
  **свои** namespace `cookbook-hardening` (`enforce: restricted`) и
  `cookbook-hardening-baseline` (`enforce: baseline`), а для одного демо seccomp — **временный**
  `cookbook-hardening-privileged` (`enforce: privileged`), и в teardown удаляет их целиком.
- Право **ставить Pod Security-метки на свои namespace**, включая
  `pod-security.kubernetes.io/enforce=privileged` — без него демо 3 не снимет `Seccomp: 0`: на
  кластере cluster-wide default `baseline` запрещает `Unconfined`, и опустить уровень можно
  только явной меткой (см. «Ограничения», пункт 3).
- Доступ **на чтение** к узлам (`get node`) — для таблицы версий. Мутаций узлов стенд не
  делает (см. «Изоляция и безопасность»).
- Возможность выполнять `kubectl exec` в свой под — маски capabilities, seccomp и разведка
  возможностей ядра снимаются изнутри пода через `/proc` и `/sys`.
- Container runtime и узел с поддержкой **seccomp** (на k8s-volga — containerd 2.2.5, seccomp
  есть и включён `SeccompDefault`). **`metrics`/Prometheus стенду не нужны** — ни одного замера
  потребления он не делает.
- Всё остальное стенд **проверяет, а не требует**: AppArmor, режим SELinux и user namespaces —
  чего нет, идёт в «Ограничения», а не в предусловия. Честно: **демо user namespaces требует
  поддержки узла, которой на k8s-volga НЕТ** (`max_user_namespaces = 0`) — оно показывает
  именно факт недоступности, а не работающую изоляцию uid.

## Таблица версий

Инфраструктурные версии — из **шапки каждой фикстуры** (`# captured on k8s-volga …`); образы —
из фикстур демо. Каждое значение сверено (столбец «Откуда»).

| Компонент | Версия | Откуда |
|---|---|---|
| Kubernetes (сервер) | `v1.36.2` | шапка фикстур, `k8s 1.36.2` (напр. `00-capabilities.txt` стр. 1) |
| Kubernetes (клиент `kubectl`) | `v1.36.1` | `kubectl version` (сверка чтением N4; в фикстурах стенда не фиксировался) |
| ОС узлов | Talos `v1.13.6` | шапка фикстур, `Talos v1.13.6` |
| Ядро узлов | `6.18.38-talos` | шапка фикстур, `kernel 6.18.38-talos`; `/proc/cmdline` в `00-capabilities.txt` |
| Среда исполнения | `containerd 2.2.5` | шапка фикстур, `containerd 2.2.5` |
| Образ подов демо 0–2 | `busybox:1.37` | `10-capabilities.txt` стр. 4, 62 |
| Образ syscall-пары демо 3 | `python:3.13-alpine` | `30-seccomp.txt` стр. 5, 61 |

Клиент на патч-версию младше сервера (`v1.36.1` против `v1.36.2`) — штатная ситуация, на
вывод стенда не влияет.

## Изоляция и безопасность

Кластер k8s-volga — **общий и живой**: три control-plane и три worker-узла, на воркерах уже
работают системные компоненты (Cilium, CoreDNS, CSI, vector) и объекты соседних стендов
серии. Стенд про безопасность — тема, где легче всего задеть и узел, и соседей, — поэтому
границы жёсткие и повторены в скриптах.

- **Два своих namespace, оба удаляются целиком.** `cookbook-hardening`
  (`enforce: restricted`) и `cookbook-hardening-baseline` (`enforce: baseline`) заводит
  `manifests/00-namespaces.yaml`, сносит `teardown.sh`. Общий для стендов серии
  `cookbook-k8s` (на нём `enforce: baseline`) стенд **не трогает** — половине демо нужен
  `restricted`, а менять уровень общего namespace нельзя. Фактические метки уровней сняты в
  `fixtures/00-ns-labels.txt`: `restricted` и `baseline` соответственно.
- **Временный `cookbook-hardening-privileged` (`enforce: privileged`) — только на одно демо.**
  Демо 3 создаёт его, снимает `Seccomp: 0` в `Unconfined`-поде и паре для изоляции syscall и
  **сносит целиком** (`kubectl delete -f manifests/31-seccomp-unconfined.yaml`); `teardown.sh`
  удаляет его страховочно. Постоянным этот namespace не остаётся — проверено чтением: сейчас
  его в кластере нет.
- **`hostPath`, `hostNetwork`, `hostPID`, `hostIPC`, `privileged` — НЕ запускаются даже в
  демо.** В стенде hardening они появляются исключительно как **отклонённые** admission-ом
  манифесты (демо 2): через `kubectl apply --dry-run=server` снимается текст отказа Pod
  Security, а работающего пода с таким признаком не создаётся. Проверено чтением: подов
  `host-path`/`host-network`/`host-pid`/`privileged-pod` во всём кластере нет.
- **Побег из контейнера не демонстрируется** ни в одной задаче стенда. Стенд показывает, чем
  контейнер ограничен, а не как эти ограничения обойти. Это осознанное ограничение на общем
  живом кластере, а не пробел.
- **Каждому namespace — своя `ResourceQuota`** со скромным потолком (`requests.cpu: 1`,
  `requests.memory: 1Gi`, `pods: 20`) и `LimitRange` с дефолтными requests — страховка от
  того, что демо оставит хвост на общем кластере.
- **Control plane — только чтение.** Тейнты, `drain`/`cordon`, метки на узлах — запрещены и в
  скриптах не встречаются. Разведка возможностей ядра снимается **изнутри своего пода**
  (`/proc`, `/sys`), а не с узла.
- **Стенды фаз 1–5 не трогаются:** `cookbook-k8s` (resources, autoscaling, stateful, secrets,
  scheduling, operators, architecture), `cookbook-net` (networking), `cookbook-argocd`
  (gitops) — стенд их только читает.

## Демо

Ось стенда — контраст уровней: контейнер по умолчанию против hardened, `baseline` против
`restricted` на одном поде, seccomp тремя режимами. Небезопасные признаки
(`hostPath`/`privileged`) показаны только как отказ admission. Четыре демо, сквозная нумерация
0–3:

- **Демо 0: что даёт кластер** — `fixtures/00-capabilities.txt`. Снято, см. раздел «Что даёт этот кластер».
- **Демо 1: по умолчанию против hardened** — `fixtures/10-capabilities.txt`, манифесты
  `10-default-pod.yaml`, `11-hardened-pod.yaml`, `12-hardened-writable.yaml`. Снято, см.
  раздел «Демо 1» ниже.
- **Демо 2: Pod Security Standards — тексты отказов, baseline против restricted** —
  `fixtures/20-psa.txt`, манифесты `20-psa-violations.yaml` (группа A, безопасные),
  `21-psa-dangerous.yaml` (группа B, опасные — только `--dry-run=server`). Снято, см.
  раздел «Демо 2» ниже.
- **Демо 3: seccomp тремя режимами и user namespaces** — `fixtures/30-seccomp.txt`, манифесты
  `30-seccomp-pods.yaml` (default/RuntimeDefault, оба baseline), `31-seccomp-unconfined.yaml`
  (Unconfined + пара для изоляции — во временном ns `enforce=privileged`, `Seccomp: 0` живьём),
  `32-userns.yaml` (`hostUsers: false` — не стартует). Снято, см. раздел «Демо 3» ниже.

## Демо 1: по умолчанию против hardened

Один и тот же образ `busybox:1.37` в двух видах: «обычный» под `default-pod` (namespace
`cookbook-hardening-baseline`, `enforce: baseline` — не требует ни non-root, ни drop caps) и
hardened-под `hardened-pod` (namespace `cookbook-hardening`, `enforce: restricted`, полный
`securityContext`). Все значения — из `fixtures/10-capabilities.txt`, снятого изнутри подов
через `/proc/self/status`. **Побег не демонстрируется — показывается маска ПРАВ, а не её
эксплуатация.**

| Показатель | `default-pod` (baseline) | `hardened-pod` (restricted) |
|---|---|---|
| `uid` (`id`) | `0` (root) | `1000` |
| `CapEff` | `00000000a80425fb` — **14** capabilities | `0000000000000000` — **0** |
| `CapBnd` (bounding set) | `00000000a80425fb` | `0000000000000000` |
| `NoNewPrivs` | `0` (можно поднять права) | `1` |
| запись в `/` (`touch /testfile`) | разрешена | `Read-only file system` |
| `Seccomp` | `2` (фильтр по умолчанию узла) | `2` (объявлен явно) |

**Дефолтная маска `CapEff = 00000000a80425fb`** — не «всё» (полный набор был бы
`000001ffffffffff`) и не «ничего»: это 14 capabilities, которые containerd выдаёт контейнеру
по умолчанию. Число снято фактом — popcount маски из `/proc` равен 14 (часто цитируемое «15» —
из памяти, здесь маска даёт 14). В `busybox` нет `capsh`, поэтому расшифровка масок в фикстуре
приведена **как разбор по документации** ядра (`include/uapi/linux/capability.h`), а не как
вывод команды; сама маска — фактический вывод. Ключевые права в дефолтной маске:

- `CAP_SETUID` / `CAP_SETGID` — сменить личность процесса (uid/gid);
- `CAP_DAC_OVERRIDE`, `CAP_CHOWN`, `CAP_FOWNER` — обойти права доступа к файлам, менять владельца;
- `CAP_NET_RAW` — RAW/PACKET-сокеты (ping, но и спуфинг/сниффинг);
- `CAP_NET_BIND_SERVICE` — слушать порты <1024; `CAP_MKNOD`, `CAP_SETPCAP`, `CAP_SETFCAP`,
  `CAP_KILL`, `CAP_FSETID`, `CAP_SYS_CHROOT`, `CAP_AUDIT_WRITE`.

`capabilities.drop: ["ALL"]` обнуляет маску **целиком**, включая bounding set (`CapBnd: 0` —
отобранное уже не вернуть даже setuid-бинарём), а `allowPrivilegeEscalation: false` даёт
`NoNewPrivs: 1`.

### readOnlyRootFilesystem: что ломает и как чинится

`readOnlyRootFilesystem: true` не «ломает приложение» — он требует **явно объявить, куда оно
пишет**. Приложение, пишущее во временный каталог, на read-only корне падает:

```text
# cmd: kubectl -n cookbook-hardening exec hardened-pod -- sh -c 'echo data > /tmp/cache.dat'
sh: can't create /tmp/cache.dat: Read-only file system
```

Штатное решение — смонтировать `emptyDir` в нужный путь (`12-hardened-writable.yaml`
монтирует его в `/tmp`). Тогда запись в `/tmp` проходит, а сам корень остаётся только на
чтение — открыт ровно один объявленный путь, не вся ФС:

```text
# cmd: kubectl -n cookbook-hardening exec hardened-writable -- sh -c 'echo data > /tmp/cache.dat && cat /tmp/cache.dat'
data
# cmd: kubectl -n cookbook-hardening exec hardened-writable -- touch /rootfile
touch: /rootfile: Read-only file system
```

## Демо 2: Pod Security Standards — тексты отказов, baseline против restricted

Pod Security Standards — это admission-контроль: apiserver отклоняет небезопасный под **до**
запуска и в тексте отказа называет нарушенный пункт дословно. Главная ценность демо — эти
формулировки и **контраст**: `baseline` и `restricted` проверяют разное. Все значения — из
`fixtures/20-psa.txt`, каждая строка — реальный вывод команды (`# cmd:`).

**Восемь подов-нарушителей, каждый нарушает ровно один пункт** (`20-psa-violations.yaml` —
группа A; `21-psa-dangerous.yaml` — группа B):

- **группа A (безопасны):** `run-as-root` (нет `runAsNonRoot`), `no-drop-caps` (нет
  `capabilities.drop:[ALL]`), `allow-priv-esc` (`allowPrivilegeEscalation: true`), `no-seccomp`
  (нет `seccompProfile`);
- **группа B (опасны):** `host-path` (`hostPath` на `/`), `host-network` (`hostNetwork: true`),
  `host-pid` (`hostPID: true`), `privileged-pod` (`privileged: true`).

> ⛔ **Группа B никогда не запускается работающим подом.** Кластер живой и общий; `hostPath`
> на `/`, `hostNetwork`, `hostPID`, `privileged` дают доступ к узлу. Показаны **только** через
> `kubectl apply --dry-run=server` — server-side dry-run прогоняет admission (вердикт получаем),
> но **ничего не пишет в etcd** (не `--dry-run=client`: клиентский admission не запускает).
> Проверено: подов `host-path`/`host-network`/`host-pid`/`privileged-pod` во всём кластере нет.

### Дословные тексты отказов restricted

```text
# cmd: kubectl apply -f manifests/20-psa-violations.yaml -n cookbook-hardening --dry-run=server
pods "run-as-root" is forbidden: violates PodSecurity "restricted:latest": runAsNonRoot != true (pod or container "box" must set securityContext.runAsNonRoot=true)
pods "no-drop-caps" is forbidden: violates PodSecurity "restricted:latest": unrestricted capabilities (container "box" must set securityContext.capabilities.drop=["ALL"])
pods "allow-priv-esc" is forbidden: violates PodSecurity "restricted:latest": allowPrivilegeEscalation != false (container "box" must set securityContext.allowPrivilegeEscalation=false)
pods "no-seccomp" is forbidden: violates PodSecurity "restricted:latest": seccompProfile (pod or container "box" must set securityContext.seccompProfile.type to "RuntimeDefault" or "Localhost")
# cmd: kubectl apply -f manifests/21-psa-dangerous.yaml -n cookbook-hardening --dry-run=server
pods "host-path" is forbidden: violates PodSecurity "restricted:latest": restricted volume types (volume "node-root" uses restricted volume type "hostPath")
pods "host-network" is forbidden: violates PodSecurity "restricted:latest": host namespaces (hostNetwork=true)
pods "host-pid" is forbidden: violates PodSecurity "restricted:latest": host namespaces (hostPID=true)
pods "privileged-pod" is forbidden: violates PodSecurity "restricted:latest": privileged (container "box" must not set securityContext.privileged=true), allowPrivilegeEscalation != false (...)
```

Отказ называет и **что** нарушено, и **как** починить (`must set …=…`). `privileged-pod` ловит
сразу два пункта: `allowPrivilegeEscalation != false` не задаётся явно, потому что API-валидация
запрещает `privileged: true` вместе с `allowPrivilegeEscalation: false`.

### ★ Контраст: baseline и restricted проверяют разное

Те же поды через `--dry-run=server` в namespace с `enforce: baseline`. Каждая ячейка — из
реального вывода:

| под | `baseline` | `restricted` |
|---|---|---|
| `run-as-root` | **проходит** | отказ: `runAsNonRoot != true` |
| `no-drop-caps` | **проходит** | отказ: `unrestricted capabilities` |
| `allow-priv-esc` | **проходит** | отказ: `allowPrivilegeEscalation != false` |
| `no-seccomp` | **проходит** | отказ: `seccompProfile` |
| `host-path` | отказ: `hostPath volumes` | отказ: `restricted volume types` |
| `host-network` | отказ: `host namespaces` | отказ: `host namespaces` |
| `host-pid` | отказ: `host namespaces` | отказ: `host namespaces` |
| `privileged-pod` | отказ: `privileged` | отказ: `privileged` + `allowPrivilegeEscalation` |

Группа A — «мягкая гигиена» пода (root, capabilities, seccomp, поднятие прав): `baseline` её
**пропускает**, `restricted` требует. Группа B — доступ к узлу: отвергают **оба** профиля.

> ⚠️ **Фактическая поправка (проверено, не по памяти).** На k8s-volga (k8s 1.36.2)
> `baseline:latest` **отклоняет `hostPath`** — текст `hostPath volumes`. Частое утверждение
> «`baseline` разрешает `hostPath`, ограничивает только `restricted`» здесь не подтвердилось:
> `hostPath` отвергают оба профиля, но **разными правилами** — `baseline` как `hostPath volumes`,
> `restricted` как `restricted volume types`.

### enforce / warn / audit

Один профиль на трёх ключах ns ведёт себя по-разному:

- **`enforce`** — жёсткий отказ, под не создаётся (`Error from server (Forbidden)`, выше).
- **`warn`** — под **создаётся**, но `kubectl` печатает предупреждение. Проверено во
  **временном** namespace `enforce=baseline` + `warn=restricted` (метки боевых ns не тронуты,
  ns удалён после снятия): `Warning: would violate PodSecurity "restricted:latest": runAsNonRoot
  != true …` и следом `pod/run-as-root created`. Тот же текст, что при enforce, но под живёт.
- **`audit`** — записи **честно не сняты**. Аудит на apiserver включён
  (`--audit-log-path=/var/log/audit/kube/kube-apiserver.log`), значит нарушения пишутся в файл
  на узле control-plane. Но Talos не даёт SSH/node-shell, а файл не отдаётся через `kubectl`
  (это не stdout пода). Прочитать его можно было бы только `hostPath`/`privileged`-подом на
  control-plane — ровно тем, что стенд **отказывается** создавать. Поэтому audit-записи не
  показаны и не выдуманы.

## Демо 3: seccomp тремя режимами и user namespaces

Демо углубляет seccomp (фильтр не «сам собой», а результат профиля/дефолта узла) и честно
закрывает тему user namespaces фактом их недоступности. Все значения — из `fixtures/30-seccomp.txt`,
каждая строка — реальный вывод (`# cmd:`).

### seccomp тремя режимами: `Seccomp 2` / `2` / `0`

Три режима — один образ busybox, у всех `drop: ["ALL"]` и `allowPrivilegeEscalation: false`,
так что права одинаковы и единственная переменная пары RuntimeDefault/Unconfined — сам профиль.
Внутри читаем `Seccomp` из `/proc/self/status`:

| под | `seccompProfile` | namespace | `Seccomp` |
|---|---|---|---|
| `seccomp-default` | не задан | baseline | **2** — узел ставит фильтр по умолчанию (SeccompDefault) |
| `seccomp-runtimedefault` | `RuntimeDefault` | baseline | **2** — тот же фильтр, объявлен явно |
| `seccomp-unconfined` | `Unconfined` | **privileged** | **0**, `Seccomp_filters: 0` — фильтра нет |

`Seccomp: 2` (`SECCOMP_MODE_FILTER`) появляется даже **без** явного профиля — это не «само
собой», а `SeccompDefault` на узле. Явный `RuntimeDefault` лишь фиксирует тот же фильтр в
манифесте. `Unconfined` снимает фильтр целиком (`Seccomp: 0`), но не в любом namespace:

> ⚠️ **Факт (снято живьём): `Seccomp: 0` достижим, но только в namespace, ЯВНО помеченном
> `enforce=privileged`.** На k8s-volga включён **cluster-wide PodSecurity default = `baseline`**,
> и `baseline` запрещает снятие seccomp. Поэтому в непомеченном, `baseline`- или `restricted`-
> namespace под с `seccompProfile: Unconfined` **отклоняется** на admission:
>
> ```text
> pods "…" is forbidden: violates PodSecurity "baseline:latest": seccompProfile (pod must not set securityContext.seccompProfile.type to "Unconfined")
> ```
>
> Что это `default` уровня кластера, а не namespace: даже непомеченный namespace `default` ловит
> `baseline:latest`. Но PSA **штатно позволяет** namespace объявить уровень **слабее** дефолта:
> в namespace с меткой `pod-security.kubernetes.io/enforce=privileged` тот же под проходит и
> изнутри показывает `Seccomp: 0`, `Seccomp_filters: 0`:
>
> ```text
> # cmd: kubectl -n cookbook-hardening-privileged exec seccomp-unconfined -- sh -c 'grep -E "Seccomp|CapEff" /proc/self/status'
> CapEff:	0000000000000000
> Seccomp:	0
> Seccomp_filters:	0
> ```
>
> Namespace `cookbook-hardening-privileged` — **временный**: демо создаёт его, снимает замер и
> сносит целиком (`kubectl delete -f manifests/31-seccomp-unconfined.yaml`); `teardown.sh` тоже
> удаляет его с `--ignore-not-found`. Формулировка «`baseline` разрешает Unconfined, ограничивает
> только `restricted`» здесь **неверна**: снятие seccomp закрывает уже `baseline`, и снять его
> можно, лишь опустив namespace до `privileged`.

### ★ Реально заблокированный именно seccomp-ом вызов: изолирован в паре RuntimeDefault/Unconfined

Раз пара RuntimeDefault/Unconfined доступна (выше), работает **эталонная** методика: один и тот
же syscall, те же (пустые) caps, разный только профиль. Два пода `python:3.13-alpine` в
`cookbook-hardening-privileged`, `drop: ["ALL"]`, `runAsNonRoot`. Caps у обоих пустые
(`CapEff: 0`, `CapBnd: 0`), различаются только `Seccomp: 2` против `0`.

Вызов — `get_mempolicy(2)` (x86_64 syscall № 239). В дефолтном профиле containerd/moby он
разрешён лишь при наличии соответствующей capability; под её сбросил. `swapon(2)` для этой цели
**не годится** — он cap-gated и даёт `EPERM` с обеих сторон; `get_mempolicy` capability не
требует, поэтому под Unconfined проходит.

```text
# под RuntimeDefault (Seccomp: 2):
get_mempolicy(NR=239) ret=-1 errno=1(Operation not permitted) mode=-1
# под Unconfined (Seccomp: 0), тот же образ, те же пустые caps:
get_mempolicy(NR=239) ret=0 errno=0(OK) mode=0
```

**Изоляция доказана.** Под Unconfined тот же вызов с теми же пустыми caps **проходит** (`ret=0`)
— значит capability для него не нужна. Следовательно `EPERM` под RuntimeDefault даёт **не**
отсутствие capability, а **seccomp-фильтр**: единственная отличающаяся переменная — профиль.
Это и есть вызов, заблокированный именно seccomp отдельно от capabilities.

### user namespaces — недоступная фича (дословная ошибка живого события)

Поле `pod.spec.hostUsers` в API 1.36 есть, admission под с `hostUsers: false` пропускает — но
узел (Talos, kernel 6.18.x) создать user namespace не может. Под `userns-pod` виснет в
`ContainerCreating`, событие `FailedCreatePodSandBox`:

```text
# cmd: kubectl -n cookbook-hardening-baseline get events --field-selector involvedObject.name=userns-pod,reason=FailedCreatePodSandBox
Failed to create pod sandbox: rpc error: code = Unknown desc = failed to create network namespace for sandbox "…": failed to start noop process for unshare: fork/exec /proc/self/exe: no space left on device
```

`unshare: … no space left on device` — `ENOSPC` при создании namespace. Причина снята фактом:

```text
# cmd: kubectl -n cookbook-hardening-baseline exec seccomp-default -- cat /proc/sys/user/max_user_namespaces
0
# cmd: kubectl -n cookbook-hardening-baseline exec seccomp-default -- cat /proc/self/uid_map
         0          0 4294967295
```

`max_user_namespaces = 0` — ядро запрещает создание user namespace вовсе. Обычный под делит
user namespace с узлом: `uid_map = 0 0 4294967295` (тождественная карта, remap нет). Показать
«uid внутри ≠ uid на узле» на k8s-volga **нельзя**: `hostUsers` — поле API, но фактически
неработоспособно (нужна поддержка узла/Talos, которой нет). Зафиксировано фактом, а не
предположением.

## Что даёт этот кластер

Снято разведкой (только чтение и временные поды, удалены после снятия) —
`fixtures/00-capabilities.txt`. **Ничего по памяти; каждый пункт — вывод команды или `/proc`.**

- **seccomp — есть и применяется.** Под с `seccompProfile: RuntimeDefault` изнутри показывает
  `Seccomp: 2` (`SECCOMP_MODE_FILTER`) и `Seccomp_filters: 1` в `/proc/1/status`. Более того,
  под **без** явного `seccompProfile` тоже получает `Seccomp: 2` — узел ставит seccomp-фильтр
  по умолчанию (поведение `SeccompDefault` на Talos), а не оставляет процесс `Unconfined`
  (что дало бы `Seccomp: 0`). Снять фильтр всё же можно — `Unconfined`-под в namespace, явно
  помеченном `enforce=privileged`, даёт `Seccomp: 0` живьём (демо 3).
- **AppArmor — нет.** Под с `appArmorProfile: RuntimeDefault` **отклоняется** kubelet-ом:
  `Cannot enforce AppArmor: AppArmor is not enabled on the host` (`status.reason: AppArmor`).
  Изнутри пода `/sys/module/apparmor/parameters/enabled` = `N`: модуль в ядре есть, но
  выключен. AppArmor — фича Debian/Ubuntu; Talos её не несёт, и это подтверждено фактом, а не
  предположением.
- **SELinux — включён на хосте, режим изнутри не читается.** `/proc/cmdline` содержит
  `selinux=1`, а процесс пода несёт SELinux-метку `system_u:system_r:pod_t:s0`
  (`/proc/1/attr/current`) — то есть SELinux на Talos активен и маркирует поды. Режим
  (`enforcing`/`permissive`) изнутри пода **не определить**: `selinuxfs`
  (`/sys/fs/selinux/`) в контейнер не смонтирован, а Talos по SSH недоступен и API режим
  SELinux узла не отдаёт. Честное ограничение разведки: факт включения снят, режим — нет.
- **user namespaces (`hostUsers: false`) — не поддержаны.** Поле `pod.spec.hostUsers` в API
  1.36 есть (`kubectl explain` его описывает), но под с `hostUsers: false` не стартует:
  `FailedCreatePodSandBox … unshare: … no space left on device`. Причина снята фактом —
  `/proc/sys/user/max_user_namespaces` = `0`: ядро запрещает создание user namespace, отсюда
  `ENOSPC` на `unshare`. Обычный под при этом делит user namespace с хостом —
  `/proc/self/uid_map` = `0 0 4294967295` (тождественная карта, remap нет).

Сводка: **seccomp — рабочий инструмент этого стенда; AppArmor и user namespaces — недоступны
и разбираются как ограничение; SELinux — включён, но его режим стенд честно не измеряет.**

## Ограничения и особенности

Девять фактов, каждый снят на этом кластере (`fixtures/`), а не взят из документации. Часть —
поправки к ходовым утверждениям, которые здесь не подтвердились.

1. **Дефолтная маска capabilities — 14, а не «15».** `CapEff` контейнера по умолчанию —
   `00000000a80425fb` (`fixtures/10-capabilities.txt`), popcount маски = **14**. Часто
   цитируемое «15 capabilities по умолчанию» — из памяти; фактический разбор битов `a80425fb`
   даёт ровно 14 (по `include/uapi/linux/capability.h`, разбор в той же фикстуре).
2. **`baseline` на этом кластере ОТКЛОНЯЕТ `hostPath`.** Это документированное поведение
   `baseline`, а не версионный квирк: `hostPath` отвергают **оба** профиля, но разными
   правилами — `baseline` как `hostPath volumes`, `restricted` как `restricted volume types`
   (`fixtures/20-psa.txt`). Ходовое «`baseline` разрешает `hostPath`, ограничивает только
   `restricted`» здесь неверно.
3. **Cluster-wide PodSecurity default = `baseline`.** Namespace **без** PSA-меток всё равно
   enforce'ит `baseline` (даже `default` ловит `baseline:latest` — `fixtures/30-seccomp.txt`).
   Чтобы снять seccomp-фильтр (`Unconfined`), namespace надо **явно** пометить
   `enforce=privileged` — PSA штатно позволяет опустить уровень ниже дефолта.
4. **`SeccompDefault` включён на kubelet.** Под **без** `seccompProfile` уже несёт `Seccomp: 2`
   (`SECCOMP_MODE_FILTER`), а не `Unconfined`/`0` (`fixtures/00-capabilities.txt`,
   `fixtures/10-capabilities.txt`, `fixtures/30-seccomp.txt`). «Нет профиля» здесь не значит
   «нет фильтра».
5. **seccomp-блокировку пришлось изолировать через `get_mempolicy`.** Он разрешён при наличии
   capability и запрещён под `RuntimeDefault` с пустыми caps — так единственной отличающейся
   переменной в паре `RuntimeDefault`/`Unconfined` остаётся профиль (`fixtures/30-seccomp.txt`).
   `swapon` для этой цели **не годится**: он cap-gated и даёт `EPERM` с обеих сторон.
6. **User namespaces недоступны.** Поле `pod.spec.hostUsers` в API 1.36 есть, но
   `hostUsers: false` падает с `unshare: … no space left on device`
   (`fixtures/30-seccomp.txt`), потому что на узле `max_user_namespaces = 0`. Нужна поддержка
   узла/Talos, которой на k8s-volga нет; «uid внутри ≠ uid на узле» показать нельзя.
7. **Audit-лог apiserver недоступен.** Аудит включён
   (`--audit-log-path=/var/log/audit/kube/kube-apiserver.log`, `fixtures/20-psa.txt`), но файл
   лежит на control-plane узле Talos без SSH и не отдаётся через `kubectl`. Прочитать его можно
   было бы только `hostPath`/`privileged`-подом — ровно тем, что стенд отказывается создавать.
   Поэтому audit-записи честно не сняты.
8. **Режим SELinux не измерен.** SELinux включён (`selinux=1` в `/proc/cmdline`, процесс несёт
   метку `system_u:system_r:pod_t:s0` — `fixtures/00-capabilities.txt`), но
   `enforcing`/`permissive` изнутри пода не читается: `selinuxfs` в контейнер не смонтирован, а
   `talosctl` к узлу нет. Факт включения снят, режим — нет.
9. **AppArmor нет.** Talos его не несёт: под с `appArmorProfile: RuntimeDefault` отклоняется
   kubelet-ом (`Cannot enforce AppArmor: AppArmor is not enabled on the host`), а
   `/sys/module/apparmor/parameters/enabled` = `N` (`fixtures/00-capabilities.txt`).

## ★ Hardening-чеклист

Итог стенда: минимальный набор для контейнера, каждый пункт которого стенд **измерил**, а не
переписал из гайда. Слева — что включить, справа — что это даёт и каким числом подтверждено
(демо 1 — `fixtures/10-capabilities.txt`, демо 2 — `fixtures/20-psa.txt`, демо 3 —
`fixtures/30-seccomp.txt`).

| Настройка `securityContext` / PSA | Что даёт (замер стенда) |
|---|---|
| `runAsNonRoot: true` + `runAsUser: 1000` | процесс не root: `id` в hardened-поде → `uid=1000`, тогда как default-под → `uid=0(root)` (демо 1) |
| `capabilities.drop: ["ALL"]` | маска прав обнуляется целиком: `CapEff` `00000000a80425fb` (14 прав) → `0000000000000000`, вместе с `CapBnd` — отобранное не вернуть (демо 1) |
| `allowPrivilegeEscalation: false` | `NoNewPrivs: 0 → 1` — setuid-бинарь больше не поднимет права (демо 1) |
| `readOnlyRootFilesystem: true` (+ `emptyDir` для записи) | корень только на чтение: `touch /testfile` → `Read-only file system`; запись открыта ровно в один объявленный путь — `emptyDir` в `/tmp` пишется, корень остаётся RO (демо 1) |
| `seccompProfile: RuntimeDefault` | seccomp-фильтр объявлен явно: `Seccomp: 2`; изолированный `get_mempolicy(239)` под ним → `EPERM`, под `Unconfined` с теми же пустыми caps → `OK` (демо 1, демо 3) |
| namespace `enforce: restricted` (PSA) | admission отклоняет всё, что не hardened, до запуска и называет пункт дословно: `runAsNonRoot != true`, `unrestricted capabilities`, `allowPrivilegeEscalation != false`, `seccompProfile` (демо 2) |
| минимум монтирований (в т.ч. секретов) | hardened-под монтирует ровно один явно объявленный `emptyDir` и ни одного секрета; RO-корень заставляет декларировать каждый путь записи (демо 1). Минимизацию именно секрет-монтирований подробно разбирает статья 4 серии — этот стенд её не переизмеряет |

## Порядок запуска

```bash
export KUBECONFIG=/path/to/k8s-volga/kubeconfig   # контекст admin@k8s-volga
cd kubernetes/hardening
./apply.sh manifests/00-namespaces.yaml           # два namespace с уровнями PSA
./capture.sh 00-ns-labels -- get ns cookbook-hardening cookbook-hardening-baseline \
  -o custom-columns='NAME:.metadata.name,ENFORCE:.metadata.labels.pod-security\.kubernetes\.io/enforce'
# разведка возможностей — задача N0, fixtures/00-capabilities.txt

# Демо 1: по умолчанию против hardened (задача N1)
./apply.sh manifests/10-default-pod.yaml        # обычный под, namespace baseline
./apply.sh manifests/11-hardened-pod.yaml       # restricted-под, namespace hardening
./apply.sh manifests/12-hardened-writable.yaml  # тот же + emptyDir в /tmp
# снятие масок изнутри — fixtures/10-capabilities.txt (grep Cap*/NoNewPrivs/Seccomp /proc/self/status)
# уборка подов демо:
kubectl -n cookbook-hardening-baseline delete pod default-pod --ignore-not-found
kubectl -n cookbook-hardening delete pod hardened-pod hardened-writable --ignore-not-found

# Демо 2: Pod Security Standards — тексты отказов и контраст baseline/restricted (задача N2)
# группа A (безопасна) и группа B (опасна) — обе через --dry-run=server (admission без записи в etcd):
kubectl apply -f manifests/20-psa-violations.yaml -n cookbook-hardening --dry-run=server            # отказы restricted
kubectl apply -f manifests/20-psa-violations.yaml -n cookbook-hardening-baseline --dry-run=server    # baseline пропускает
kubectl apply -f manifests/21-psa-dangerous.yaml  -n cookbook-hardening --dry-run=server            # ⛔ ТОЛЬКО dry-run
kubectl apply -f manifests/21-psa-dangerous.yaml  -n cookbook-hardening-baseline --dry-run=server   # ⛔ ТОЛЬКО dry-run
# enforce vs warn — во ВРЕМЕННОМ ns (создать, снять предупреждение, удалить):
kubectl create ns cookbook-hardening-warn
kubectl label ns cookbook-hardening-warn pod-security.kubernetes.io/enforce=baseline \
  pod-security.kubernetes.io/warn=restricted pod-security.kubernetes.io/warn-version=latest --overwrite
kubectl apply -f manifests/20-psa-violations.yaml -n cookbook-hardening-warn   # Warning + под создаётся
kubectl delete ns cookbook-hardening-warn                                       # уборка временного ns
# снятие — fixtures/20-psa.txt

# Демо 3: seccomp тремя режимами и user namespaces (задача N3)
./apply.sh manifests/30-seccomp-pods.yaml        # default + RuntimeDefault, namespace baseline
kubectl -n cookbook-hardening-baseline wait --for=condition=Ready pod/seccomp-default pod/seccomp-runtimedefault --timeout=60s
# режимы с фильтром (Seccomp: 2). sh -c 'grep …' — надёжнее прямого grep (Git Bash не манглит /proc):
kubectl -n cookbook-hardening-baseline exec seccomp-default        -- sh -c 'grep -E "Seccomp|NoNewPrivs|CapEff" /proc/self/status'
kubectl -n cookbook-hardening-baseline exec seccomp-runtimedefault -- sh -c 'grep -E "Seccomp|NoNewPrivs|CapEff" /proc/self/status'
# Граница дефолта: в baseline-ns Unconfined-под отклоняется на admission (dry-run, без записи в etcd):
kubectl -n cookbook-hardening-baseline apply --dry-run=server -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata: {name: unconf-test}
spec:
  restartPolicy: Never
  securityContext: {seccompProfile: {type: Unconfined}}
  containers: [{name: box, image: busybox:1.37, command: ["sleep","3600"], securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}}]
YAML
# ВРЕМЕННЫЙ ns enforce=privileged + Unconfined-под + пара для изоляции seccomp-блокировки:
./apply.sh manifests/31-seccomp-unconfined.yaml
kubectl -n cookbook-hardening-privileged wait --for=condition=Ready pod/seccomp-unconfined pod/syscall-runtimedefault pod/syscall-unconfined --timeout=120s
# Seccomp: 0, Seccomp_filters: 0 — фильтр снят:
kubectl -n cookbook-hardening-privileged exec seccomp-unconfined -- sh -c 'grep -E "Seccomp|NoNewPrivs|CapEff" /proc/self/status'
# изоляция seccomp-блокировки: get_mempolicy(239) — EPERM под RuntimeDefault, OK под Unconfined (те же пустые caps):
PY='import ctypes,os; l=ctypes.CDLL(None,use_errno=True); m=ctypes.c_int(-1); ctypes.set_errno(0); r=l.syscall(239,ctypes.byref(m),None,0,None,0); e=ctypes.get_errno(); print("get_mempolicy(NR=239) ret=%d errno=%d(%s) mode=%d"%(r,e,(os.strerror(e) if e else "OK"),m.value))'
kubectl -n cookbook-hardening-privileged exec syscall-runtimedefault -- python3 -c "$PY"
kubectl -n cookbook-hardening-privileged exec syscall-unconfined     -- python3 -c "$PY"
# уборка временного privileged-ns целиком (ns не должен оставаться с работающими подами):
kubectl delete -f manifests/31-seccomp-unconfined.yaml --ignore-not-found
# user namespaces — под НЕ поднимется, снимаем событие:
./apply.sh manifests/32-userns.yaml
sleep 8
kubectl -n cookbook-hardening-baseline get events --field-selector involvedObject.name=userns-pod,reason=FailedCreatePodSandBox
kubectl -n cookbook-hardening-baseline exec seccomp-default -- sh -c 'cat /proc/sys/user/max_user_namespaces'
kubectl -n cookbook-hardening-baseline exec seccomp-default -- sh -c 'cat /proc/self/uid_map'
# уборка подов демо 3:
kubectl -n cookbook-hardening-baseline delete pod seccomp-default seccomp-runtimedefault userns-pod --ignore-not-found
# снятие — fixtures/30-seccomp.txt
```

Скрипты: `apply.sh` проверяет контекст `admin@k8s-volga` и применяет манифест; `capture.sh`
пишет живой вывод в `fixtures/<name>.txt` с шапкой `# cmd:`; демо N1–N3 добавят свои
манифесты в `manifests/` и фикстуры.

## Teardown

```bash
./teardown.sh
```

Удаляет **оба** постоянных namespace целиком (`cookbook-hardening`,
`cookbook-hardening-baseline`) плюс **временный** `cookbook-hardening-privileged` (если демо 3
его не снесло) — все с `--ignore-not-found`, идемпотентно. В `kube-system` не заходит, на узлах
ничего не меняет (ни меток, ни тейнтов, ни `cordon`/`drain`), общие namespace серии не трогает.
