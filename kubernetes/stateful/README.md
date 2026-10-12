# Стенд: StatefulSet и хранилище (k8s-volga)

Живые замеры для статьи серии «Kubernetes на практике»: собственный том на каждую реплику,
предсказуемые имена и адреса подов, изоляция данных между репликами StatefulSet.

## Назначение

StatefulSet, в отличие от Deployment, даёт репликам стабильную идентичность: имя пода не
меняется при пересоздании (`demo-0`, `demo-1`, `demo-2`), у каждой реплики — свой отдельный
PVC (не общий том), а headless-сервис позволяет обращаться к конкретной реплике по DNS-имени,
а не только через виртуальный IP балансировщика. Демо этого стенда показывает три опоры этой
модели живьём: предсказуемые имена, по одному PVC на реплику и адресуемость каждого пода.

## Требования к кластеру

- Kubernetes 1.36+.
- StorageClass с динамическим провижинингом — на этом кластере (k8s-volga) он ровно один,
  `nfs-csi` (`nfs.csi.k8s.io`, бэкенд NFS), и его свойства определяют часть демо этой серии:
  `reclaimPolicy: Retain` (удалённый PVC не стирает данные — PV остаётся в `Released`, за собой
  нужно прибирать вручную, это делает `teardown.sh`), `volumeBindingMode: Immediate`,
  `allowVolumeExpansion` не включён, доступен `ReadWriteOnce` (в кластере есть и `RWX` — NFS
  это позволяет, но текущее демо использует `RWO`, по одному тому на реплику). CRD снапшотов
  (`VolumeSnapshot`) в кластере отсутствуют.
- Namespace `cookbook-k8s` уже существует (создан стендом `../resources`, общий для всех
  стендов серии) — с `ResourceQuota cookbook-quota` и `LimitRange cookbook-limits`. Этот стенд
  свой namespace не создаёт.

## Таблица версий

| Компонент | Версия |
|---|---|
| Kubernetes | 1.36.2 |
| busybox | `1.37` |
| PostgreSQL | `postgres:18.4` |
| StorageClass | `nfs-csi` (`nfs.csi.k8s.io`) |

## Изоляция и безопасность

- Все объекты стенда — в существующем namespace `cookbook-k8s`, с меткой `app: demo`.
- Что не трогаем: `ns cookbook-k8s` целиком (общий с `resources` и `autoscaling` — не удаляем,
  чужие объекты в нём не затрагиваем), `ResourceQuota`/`LimitRange` namespace, любые системные
  namespace кластера k8s-volga (`kube-system`, `argocd`, `monitoring`, `cilium`,
  `external-secrets`, `sealed-secrets`, `vpa` и т. п.).
- `teardown.sh` удаляет только объекты этого стенда (StatefulSet, Service, PVC и освободившиеся
  после них PV — подробный список см. в разделе Teardown ниже) — namespace остаётся.

## Демо

Базовая конструкция (задача D0):

- **`manifests/00-headless-svc.yaml`** — headless-сервис `demo` (`clusterIP: None`), даёт
  DNS-имя на каждую реплику по отдельности.
- **`manifests/10-statefulset.yaml`** — StatefulSet `demo`, 3 реплики, `volumeClaimTemplates`
  на 1Gi (`nfs-csi`), каждая реплика при старте дописывает свою первую запись в
  `/data/journal.txt` на своём томе.
- **`fixtures/10-statefulset.txt`** — живой снимок: три пода `demo-0`/`demo-1`/`demo-2` с
  предсказуемыми именами; три отдельных `Bound` PVC `data-demo-0`/`data-demo-1`/`data-demo-2`
  на разных PV; у всех PV `RECLAIM: Retain`; журнал `demo-0` и `demo-1` содержит только
  собственную запись (тома не общие — реплика не видит записи соседей); адрес каждой реплики
  получен через headless-сервис двумя способами: поимённый DNS
  (`demo-0.demo.cookbook-k8s.svc.cluster.local` → IP пода `demo-0`, и так для всех трёх) и
  `EndpointSlice` сервиса `demo` (все три адреса подов разом, без единого ClusterIP).

> **Нюанс DNS в busybox**: короткое имя пода без метки сервиса (`demo-1`) изнутри busybox не
> резолвится — резолвер перебирает search-суффиксы из `/etc/resolv.conf` по одному
> (`demo-1.cookbook-k8s.svc.cluster.local`, `demo-1.svc.cluster.local`, `demo-1.cluster.local`),
> а настоящая запись пода — `demo-1.demo.cookbook-k8s.svc.cluster.local` (имя сервиса — отдельная
> вторая метка), которую такой перебор не строит. Работает только полный FQDN. Это особенность
> резолвера busybox (обработка `ndots`/`search`), а **не** общее свойство headless-сервисов —
> сама DNS-запись валидна и рассчитана на обращение по полному имени в любом резолвере.
> Подробности и живые запросы — в `fixtures/10-statefulset.txt`.

Идентичность и порядок обновления (задача D1):

- **`fixtures/20-identity-order.txt`** — стабильная идентичность и порядок обновления: под
  `demo-1` удалён и пересоздан под тем же именем, тот же PVC `data-demo-1`
  (`uid`/`creationTimestamp`/`volume` не изменились), журнал `/data/journal.txt` пережил
  удаление (первая запись из D0, метка до удаления и новая запись нового пода — все три
  строки в одном файле). Обновление StatefulSet (patch аннотации шаблона) шло строго по
  одному поду одновременно, в порядке от старшего индекса к младшему: `demo-2` → `demo-1` →
  `demo-0` (`podManagementPolicy: OrderedReady`, `updateStrategy: RollingUpdate` с
  `partition: 0` — дефолтные значения, в манифесте не заданы явно).

Судьба PVC при скейле и `persistentVolumeClaimRetentionPolicy` (задача D2):

- **`manifests/20-pvc-retention.yaml`** — второй, независимый StatefulSet `demo-rp`
  (2 реплики, свой headless-сервис, лейбл `app: demo-rp`, тот же образ/логика записи в
  `journal.txt`, те же ресурсы, тот же `volumeClaimTemplates` на `nfs-csi`/1Gi), отличающийся
  от `demo` только явным
  `persistentVolumeClaimRetentionPolicy: {whenDeleted: Delete, whenScaled: Delete}`. Стенд
  `demo-rp` — временный, только для этой демонстрации: поднимается
  (`./apply.sh manifests/20-pvc-retention.yaml`) и полностью удаляется после снятия фикстуры
  (`kubectl delete statefulset/service demo-rp` + прибранные `Released`-PV); в отличие от
  `demo` он не остаётся развёрнутым в кластере.
- **`fixtures/30-scale-pvc.txt`** — обе картины рядом, живьём:
  - **По умолчанию (StatefulSet `demo`, без политики)**: скейл `3 → 1` реплики оставляет ВСЕ
    три PVC (`data-demo-0/1/2`) в `Bound` — контроллер не удаляет тома вслед за подами.
    При возврате `1 → 3` под `demo-2` пересоздаётся и подхватывает тот же PVC: в
    `journal.txt` видна метка `метка перед скейлом вниз 2026-07-23T11:32:01Z`, оставленная
    ДО скейла вниз, а следом — новая запись `demo-2 первая запись 2026-07-23T11:33:12Z` от
    заново стартовавшего контейнера. Это доказывает, что данные не потеряны и подключён
    старый том, а не создан новый.
  - **С `whenScaled: Delete` (StatefulSet `demo-rp`)**: тот же скейл `2 → 1` реплики удаляет
    PVC освободившейся реплики (`data-demo-rp-1`). Удаление не мгновенно — через 30 секунд
    после скейла PVC ещё был `Bound`, реально пропал только к отметке ~50–60 секунд.
  - **С `whenDeleted: Delete`**: удаление StatefulSet `demo-rp` целиком (`kubectl delete
    statefulset demo-rp`) снесло следом оставшиеся PVC — сначала `Terminating`, затем
    полностью пропали (`No resources found`). Пересъёмка с отметками времени (`demo-rp`
    пересоздан заново, опрос PVC каждые 5 секунд после `kubectl delete`): удаление начато в
    `2026-07-23T11:43:59Z`, в `11:44:27Z` (28 с спустя) PVC ещё `Terminating`, в
    `11:44:32Z` (33 с спустя, следующий опрос) — уже `No resources found`. Фактическое
    время удаления укладывается в интервал 28–33 секунды — точнее не измерить при шаге
    опроса 5 секунд (см. `fixtures/30-scale-pvc.txt`, блок «ПЕРЕСНЯТО»).
  - В обоих случаях удаление PVC у `demo-rp` НЕ убирает PV: `nfs-csi` — `reclaimPolicy:
    Retain`, освободившийся PV переходит в `Released` (в первой съёмке — `pvc-33595ed6-…`
    для `data-demo-rp-1`, `pvc-678a019a-…` для `data-demo-rp-0`; в пересъёмке этот переход
    в `Released` снят явным `kubectl get pv` для обоих новых PV сразу после исчезновения
    PVC) и данные на NFS остаются — политика удаления PVC регулирует судьбу заявки, а не
    самого тома на бэкенде; `Released`-тома этого демо прибраны вручную (см. Teardown; PV
    пересъёмки уже удалены как часть уборки после измерения).

Практический смысл: по умолчанию `persistentVolumeClaimRetentionPolicy` не задана — тома
переживают скейл вниз, это осознанная защита от случайной потери данных при
масштабировании. Оборотная сторона — тома накапливаются: уменьшили реплики один раз,
забыли — платите за хранилище неиспользуемых PVC до следующей ручной уборки.
`whenScaled: Delete` уместен там, где реплика без пода не нужна (кеш, воркер с
восстановимым состоянием); `whenDeleted: Delete` — там, где жизненный цикл данных явно
совпадает с жизненным циклом StatefulSet целиком (например, staging-окружение, поднимаемое
и сносимое целиком). Для БД и другого состояния, которое дороже восстановить, чем хранить, —
политику по умолчанию (без `persistentVolumeClaimRetentionPolicy`, как у `demo`) обычно и
оставляют, а PVC подчищают осознанно и вручную.

Три ограничения хранилища, которые задаёт конкретный StorageClass, а не Kubernetes вообще
(задача D3):

- **`manifests/30-rwx-shared.yaml`** — отдельный PVC `shared` (`ReadWriteMany`, `nfs-csi`,
  1Gi) и два обычных пода (не StatefulSet) `rwx-writer`/`rwx-reader`, оба монтируют его в
  `/shared`; `podAntiAffinity` на лейбле `app: rwx-demo` разводит их по разным узлам, чтобы
  замер RWX не свёлся к случаю «оба пода на одном узле». Временный, только для этой
  демонстрации — том основного StatefulSet (`data-demo-*`) не трогается ни в одном из трёх
  пунктов ниже.
- **`fixtures/40-storage-limits.txt`** — три демо подряд, живьём:
  - **Свойства `nfs-csi`**: `RECLAIM: Retain`, `BINDING: Immediate`, `EXPAND: <none>`
    (`allowVolumeExpansion` в StorageClass не задан — это опора для следующего пункта).
  - **Отказ на расширение**: `kubectl patch pvc data-demo-0` с `1Gi` → `2Gi` вернул
    `Error from server (Forbidden): persistentvolumeclaims "data-demo-0" is forbidden: only
    dynamically provisioned pvc can be resized and the storageclass that provisions the pvc
    must support resize`; размер PVC после попытки — по-прежнему `1Gi`. Отказ — из-за
    `allowVolumeExpansion` конкретного StorageClass, а не из-за того, что Kubernetes не умеет
    расширять тома вообще (умеет, если StorageClass это разрешает).
  - **RWX на NFS**: PVC `shared` — `[ReadWriteMany]`, `Bound`; `rwx-writer` оказался на
    `k8s-volga-wk02`, `rwx-reader` — на `k8s-volga-wk01` (разные узлы, `podAntiAffinity`
    сработал). Писатель дописал строку в `/shared/shared.txt`, читатель с другого узла
    прочитал ровно эту строку (`запись из writer 2026-07-23T11:51:03Z`) — реальное
    подтверждение, что том одновременно смонтирован на двух узлах, а не просто «два пода
    видят один PVC по очереди». RWX здесь доступен именно потому, что `nfs-csi` — файловое
    (NFS) хранилище; на блочном хранилище (типичный cloud-диск, Ceph RBD и т. п.)
    `ReadWriteMany` обычно недоступен вообще.
  - **`Retain` на томе `shared`**: до удаления PVC — PV `pvc-db95e396-…` в `Bound`; поды
    удалены, PVC `shared` удалён, через 15 секунд PV того же имени — уже не привязан к PVC
    (нет claim в статусе `Bound`), а в `Released`, с той же политикой `Retain`. PV не исчез
    вместе с PVC — данные на NFS не тронуты, освободившийся том прибран вручную (см. ниже),
    ровно так же, как это уже показано на `demo-rp` в задаче D2, но здесь — на отдельном RWX
    томе, чтобы не задевать тома `demo`.
  - После снятия фикстуры освободившийся PV `pvc-db95e396-…` удалён вручную (`kubectl delete
    pv`); PVC/поды `shared`/`rwx-writer`/`rwx-reader` этого демо больше не существуют в
    кластере — временные, в отличие от `demo`, который остался развёрнутым 3/3.

Логический бэкап и восстановление (задача D4):

- **`manifests/40-postgres.yaml`** — отдельный, временный StatefulSet `pg` (1 реплика,
  headless-сервис `pg`, образ `postgres:18.4`, `PGDATA=/var/lib/postgresql/data/pgdata` —
  подкаталог, а не корень тома, иначе инициализация на смонтированном пустом каталоге NFS не
  проходит), `volumeClaimTemplates` на `nfs-csi`/1Gi/`RWO`, как у `demo`. Единственное
  отличие от прежних StatefulSet стенда — `securityContext` с явными `runAsUser`/`runAsGroup`/
  `fsGroup`: экспорт `nfs-csi` сквошит любой клиентский uid/gid к фиксированному `1024:100`
  (типичная настройка Synology-шары «map all users to admin» — проверено: файл, созданный
  процессом с uid 999, на диске всё равно оказывается `1024:100`). Postgres сверяет владельца
  `PGDATA` с эффективным uid процесса побайтово, поэтому контейнер обязан стартовать ровно под
  `1024:100` — иначе `initdb` падает с `FATAL: data directory "..." has wrong ownership`
  (реально воспроизведено на попытке с uid/gid `999:999`, штатным uid образа `postgres`, прежде
  чем стало ясно, в какой uid сквошит эта NFS-шара).
- **`fixtures/50-backup-restore.txt`** — демо снято живьём, числа и текст ошибки настоящие:
  - **Отсутствие снапшотов тома**: `kubectl api-resources --api-group=snapshot.storage.k8s.io`
    вернул пустой список ресурсов, `kubectl get crd | grep -i snapshot` — вообще ничего. В
    кластере k8s-volga нет ни одной CRD группы `snapshot.storage.k8s.io` — значит нет
    `VolumeSnapshotClass`/`VolumeSnapshot`, и продемонстрировать снапшот тома на уровне
    CSI здесь нечем. Это не свойство Kubernetes вообще (снапшоты — штатный, широко
    поддерживаемый механизм CSI при установленном snapshot-controller и CRD), а
    конфигурация конкретно этого кластера: контроллер снапшотов сюда не поставлен. Поэтому
    ниже показан логический бэкап (`pg_dump`/`pg_restore`), а не снапшот тома.
  - **1000 строк до бэкапа**: таблица `notes` наполнена `generate_series(1,1000)`,
    `SELECT count(*)` вернул `1000`.
  - **Дамп**: `pg_dump -Fc` — логический дамп в кастомном формате pg_dump, размер `6957`
    байт.
  - **Потеря данных**: `DROP TABLE notes;`, повторный `SELECT count(*) FROM notes;` упал с
    `ERROR: relation "notes" does not exist` — таблицы больше нет, данные пропали.
  - **Восстановление**: `pg_restore` из дампа, `SELECT count(*)` снова вернул `1000` —
    те же строки, восстановленные из логического бэкапа, а не пересозданные заново.
  - **Пересоздание пода — отдельный, более слабый механизм**: `kubectl delete pod pg-0`,
    после возврата `1/1` — `SELECT count(*)` сразу вернул `1000`, без единого шага
    восстановления. PVC `data-pg-0` остался подключён к тому же PV
    (`pvc-b85e3eb7-…`) — под потерял процесс, но не том. Это две разные защиты: том
    переживает перезапуск/пересоздание пода сам по себе (PVC не привязан к жизненному
    циклу пода), а вот от `DROP TABLE`, `UPDATE` без `WHERE` и другой логической порчи
    данных том не спасает вообще — файлы на диске меняются штатно, ошибки нет, спасает
    только бэкап, снятый ДО инцидента.
- После снятия фикстуры StatefulSet `pg`, Service `pg` и PVC `data-pg-0` удалены, освободившийся
  PV (`pvc-b85e3eb7-…`, `Retain` → `Released`) удалён вручную; локальный файл дампа
  (`/tmp/pg-backup.dump`) в репозиторий не попадает. Основной стенд `demo` это демо не
  затронуло, он остался развёрнутым 3/3.

Доказательство uid-сквоша NFS отдельным артефактом (задача D5):

- **`fixtures/60-nfs-ownership.txt`** — временные PVC/под `nfs-ownership-check` (`nfs-csi`,
  1Gi, `securityContext.runAsUser/runAsGroup: 999` — штатный uid образа `postgres`) подняты
  отдельно от `pg`, чтобы снять факт, а не пересказать его: `id` внутри пода вернул
  `uid=999 gid=999`, а `ls -lan` на смонтированном томе после `echo test > /data/testfile`
  показал владельца `1024 100` и у файла, и у самого каталога `/data` — процесс писал под
  999:999, файл на диске лёг под 1024:100. Это и есть сквош, вживую, а не по памяти.
  Дополнительно дёшево воспроизведён и текст ошибки Postgres: временный под
  `postgres:18.4` с тем же `runAsUser/runAsGroup: 999` и `PGDATA` на этом же томе дошёл до
  `initdb`, создал конфиги и упал с `FATAL: data directory "/data/pgfail" has wrong
  ownership` — тем же текстом, что и в задаче D4 (`fixtures/40-postgres.yaml`,
  `runAsUser: 1024`/`runAsGroup: 100` у `pg` — единственное отличие от этого временного
  пода, и именно оно чинит сквош). Оба временных объекта и освободившийся после них PV
  удалены после съёмки; `demo` не затронут.

## Ограничения

Собрано в одном месте — это не баги стенда, а свойства конкретного кластера k8s-volga
(единственного доступного StorageClass `nfs-csi` и его NFS-бэкенда), которые сформировали
часть демо и на другом кластере воспроизведутся иначе или не воспроизведутся вовсе:

- **Расширение томов недоступно** — `kubectl patch pvc ... 1Gi → 2Gi` возвращает `Forbidden`
  (см. `fixtures/40-storage-limits.txt`). Это следствие `allowVolumeExpansion`, не заданного у
  конкретного StorageClass `nfs-csi`, а не ограничение Kubernetes: с другим StorageClass
  (`allowVolumeExpansion: true`) расширение работает.
- **`Retain` защищает данные ценой ручной уборки** — удалённый PVC не стирает данные на NFS,
  но освободившийся PV переходит в `Released` и остаётся в кластере, пока его явно не удалят
  (`fixtures/30-scale-pvc.txt`, `fixtures/40-storage-limits.txt`). Тома копятся, если про
  уборку забыть — `teardown.sh` в этом стенде подчищает их вручную по `claimRef`.
- **Снапшотов тома в демо нет** — в k8s-volga не установлены CRD группы
  `snapshot.storage.k8s.io` (`fixtures/50-backup-restore.txt`: `kubectl get crd | grep -i
  snapshot` — пусто), поэтому продемонстрирован только логический бэкап `pg_dump`/`pg_restore`.
  На кластере со снапшот-контроллером и нужными CRD добавляется ещё один, более быстрый способ
  защиты (снапшот CSI-тома), не показанный здесь.
- **RWX работает благодаря файловой NFS** — `ReadWriteMany` для PVC `shared` подтверждён живьём
  на двух разных узлах (`fixtures/40-storage-limits.txt`). Это свойство файлового хранилища;
  на типичном блочном (облачный диск, Ceph RBD и т. п.) `ReadWriteMany` обычно недоступен вообще.
- **NFS сквошит uid/gid к `1024:100`** — `fixtures/60-nfs-ownership.txt`: процесс с `uid=999
  gid=999` создаёт на томе файл с владельцем `1024:100`. Postgres сверяет владельца `PGDATA`
  побайтово, поэтому БД на этом хранилище требует явного `securityContext.runAsUser/runAsGroup:
  1024/100` (см. `manifests/40-postgres.yaml`) — иначе `initdb` падает с `FATAL: data directory
  ... has wrong ownership`. Это настройка конкретной NFS-шары («map all users to admin»), а не
  общее свойство файловых томов — шара с сохранением исходного uid/gid не потребовала бы этого
  обхода.

## Порядок запуска с нуля

Базовый стенд (остаётся развёрнутым, на нём строятся следующие задачи серии):

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl config current-context   # должен быть admin@k8s-volga

./apply.sh manifests/00-headless-svc.yaml
./apply.sh manifests/10-statefulset.yaml
kubectl -n cookbook-k8s rollout status statefulset/demo --timeout=300s

./capture.sh 10-statefulset -- get pod,pvc -n cookbook-k8s -l app=demo -o wide
```

Дальше — идентичность и порядок обновления (`fixtures/20-identity-order.txt`): удаление и
пересоздание пода `demo-1`, затем `kubectl patch statefulset demo` (аннотация шаблона) со
снимками каждые 10 секунд, снято прямо поверх базового стенда, без отдельного манифеста.

Оставшиеся демо поднимают свои временные объекты и полностью убирают их вручную сразу после
съёмки фикстуры (в отличие от `demo`, который остаётся):

```bash
# D2 — судьба PVC при скейле: временный StatefulSet demo-rp
./apply.sh manifests/20-pvc-retention.yaml
./capture.sh 30-scale-pvc -- get pod,pvc -n cookbook-k8s -l app=demo
# ... скейл, снятие PVC/PV, затем вручную:
kubectl -n cookbook-k8s delete statefulset demo-rp
kubectl -n cookbook-k8s delete service demo-rp

# D3 — RWX/расширение/Retain: временный PVC shared и поды rwx-writer/rwx-reader
./apply.sh manifests/30-rwx-shared.yaml
./capture.sh 40-storage-limits -- get pvc shared -n cookbook-k8s
# ... запись/чтение, снятие PV после удаления PVC, затем вручную:
kubectl -n cookbook-k8s delete pod rwx-writer rwx-reader
kubectl -n cookbook-k8s delete pvc shared

# D4 — логический бэкап: временный StatefulSet pg
./apply.sh manifests/40-postgres.yaml
kubectl -n cookbook-k8s rollout status statefulset/pg --timeout=300s
./capture.sh 50-backup-restore -- exec -n cookbook-k8s pg-0 -- psql -U postgres -c 'select 1'
# ... pg_dump/DROP TABLE/pg_restore, снятие PV после удаления PVC, затем вручную:
kubectl -n cookbook-k8s delete statefulset pg
kubectl -n cookbook-k8s delete service pg

# D5 — доказательство uid-сквоша: временный PVC/под nfs-ownership-check без манифеста в репо
# (создаётся kubectl apply -f - из инлайн-YAML, см. README-раздел выше), затем вручную
kubectl -n cookbook-k8s delete pod nfs-ownership-check nfs-ownership-pg-fail
kubectl -n cookbook-k8s delete pvc nfs-ownership-check
```

После каждого из этих временных демо PV с `Retain` остаётся `Released` — его надо удалить
вручную (`kubectl delete pv <имя>`) по `claimRef`, иначе тома копятся. Живые выводы для фикстур
снимались через `./capture.sh <name> -- <kubectl-команда>`, полные команды приведены дословно
в комментариях самих фикстур (`fixtures/*.txt`, строки `# cmd:`).

## Teardown

```bash
bash teardown.sh
```

Скрипт покрывает весь стенд, включая временные объекты отдельных задач — на случай, если
ручная уборка после какой-то из демонстраций не была доведена до конца:

- StatefulSet `demo` (базовый, остаётся развёрнутым до явного teardown), `demo-rp` (D2), `pg`
  (D4) — и их headless-сервисы;
- PVC `data-demo-*` (по метке `app: demo`), `data-pg-0` (D4), `shared` (D3),
  `nfs-ownership-check` (D5);
- поды `rwx-writer`/`rwx-reader` (D3, не под управлением StatefulSet — удаляются отдельно),
  `nfs-ownership-check`/`nfs-ownership-pg-fail` (D5);
- освободившиеся PV с `claimRef` на PVC этого стенда в `cookbook-k8s` (из-за `Retain` они не
  удаляются автоматически вместе с PVC).

`data-pg-0` удаляется отдельной явной командой, а не по метке и не каскадом: у `demo-rp`
(задача D2) `persistentVolumeClaimRetentionPolicy: {whenDeleted: Delete}` задана явно, поэтому
удаление StatefulSet само сносит его PVC следом; у `pg` этой политики нет (см. `manifests/40-
postgres.yaml`), поэтому его PVC переживает удаление StatefulSet и остаётся `Bound` без
владельца — секция очистки освободившихся PV его не подхватит, потому что её фильтр отбирает
PV в статусе `Released`, а не `Bound`.

Все команды на `--ignore-not-found` — скрипт идемпотентен, повторный запуск на уже убранном
стенде ничего не ломает. Namespace `cookbook-k8s` не удаляется — общий с другими стендами
серии; операторы и чужие объекты namespace (`ResourceQuota`, `LimitRange`, стенды
`resources`/`autoscaling`) скрипт не трогает.
