# Стенд: GitOps с ArgoCD — sync, self-heal и progressive delivery (k8s-volga)

Живые замеры для серии «Kubernetes на практике»: как декларативная доставка ведёт себя, когда
кластер и репозиторий расходятся. Стенд ставит **собственный** ArgoCD в **собственный**
namespace и даёт ему **собственный** источник правды — git-сервер, поднятый тут же, в кластере.

## Назначение

Показать GitOps не как «`kubectl apply` по кнопке из CI», а как то, чем он является: контур
непрерывной сверки желаемого состояния (репозиторий) с фактическим (кластер). Отсюда все
интересные вопросы стенда: что именно ArgoCD считает расхождением, как ведёт себя self-heal
при ручной правке, что происходит с ресурсом, удалённым из репозитория (prune), и как поверх
этого работает progressive delivery — canary и blue-green через Argo Rollouts.

## Требования к кластеру

- Kubernetes 1.36+ (на k8s-volga — `v1.36.2`).
- Права `create`/`delete` на **namespace**, `ResourceQuota` и `LimitRange`: стенд создаёт свой
  namespace `cookbook-argocd` и в teardown удаляет его целиком.
- Права на **кластерные CRD** — но только на установку и удаление CRD Argo Rollouts (M4).
  CRD `argoproj.io`, уже стоящие в кластере под боевой ArgoCD, стенд не трогает: см. «Изоляция».
- Доступ **на чтение** к namespace `argocd` (боевой ArgoCD) и к его CRD `argoproj.io`. Условие
  надо назвать честно: **несколько замеров и решений стенда опираются на то, что боевой ArgoCD в
  кластере УЖЕ стоит**. Его CRD `argoproj.io` общие, поэтому ArgoCD стенда ставится без CRD и с
  пином версии `v3.4.1` — ровно той, что у боевого; критерий изоляции сформулирован как «боевых
  `Application` было 0 — должно остаться 0». На чистом кластере, где ArgoCD ещё нет, этот стенд
  ставился бы иначе — со штатным `install.yaml` и своими CRD (см. «Ограничения», пункт 1).
- **`metrics`/Prometheus для анализа M4.** Анализ canary целится в живой Prometheus **стендов
  фазы 1** (`prometheus.cookbook-k8s.svc.cluster.local:9090`, метрика
  `max(up{job="cookbook-k8s-pods"})`). Без него автооткат нечем запустить настоящим измерением —
  «Демо 4». Демо 0–3 Prometheus не требуют.
- Исходящая сеть **с узлов** до `docker.io`, `registry.k8s.io`, `quay.io` и `ghcr.io` — образы
  тянутся оттуда. `public.ecr.aws`, откуда штатный манифест ArgoCD берёт redis, на k8s-volga
  недоступен, поэтому установка переписывает реестр этого образа — см. «Демо 1».
  Внешний git-хостинг **не нужен**: источник правды живёт внутри кластера.
- Исходящая сеть **с машины оператора** до `raw.githubusercontent.com` — оттуда качается
  манифест ArgoCD нужной версии. При недоступности скрипт повторяет попытку через прокси
  (`STAND_PROXY`, по умолчанию `s9.khorost.tech:3128`); можно вообще подсунуть локальную
  копию файла через `ARGOCD_MANIFEST=/path/to/namespace-install.yaml`.
- Свободных ресурсов под потолок стенда: `requests.cpu 2`, `requests.memory 3Gi`.
  Потолок по `limits` — `8` CPU / `8Gi`, и это не про реальное потребление: стенд в покое ест
  14m CPU. Почему цифры такие и почему первая редакция квоты не пустила ArgoCD — «Демо 1».

## Таблица версий

Все значения — из фикстур `fixtures/`; сверено по каждой строке (столбец «Откуда»).

| Компонент | Версия | Откуда |
|---|---|---|
| Kubernetes (сервер) | `v1.36.2` | `00-git-server.txt`, блок «версии окружения» |
| Kubernetes (клиент `kubectl`) | `v1.36.1` | `00-git-server.txt`, блок «версии окружения» |
| ОС узлов | Talos `v1.13.6` (все шесть) | `00-git-server.txt`, блок «версии окружения» |
| Образ git-сервера | `buildpack-deps:bookworm-scm`, дайджест `sha256:877e9e4d…c750d8` | `00-git-server.txt`, блок «поиск образа» |
| `git` в образе git-сервера | `2.39.5` | `00-git-server.txt` |
| `git` во временном поде-клиенте | `2.49.1` (`alpine/git:v2.49.1`) | `00-git-server.txt` |
| Демо-приложение в репозитории | `registry.k8s.io/pause:3.10` | `00-git-server.txt` |
| **Боевой** ArgoCD кластера (не стенда) | `quay.io/argoproj/argocd:v3.4.1` | `00-git-server.txt`, блок «версии окружения» |
| ArgoCD стенда | `quay.io/argoproj/argocd:v3.4.1` (пин; манифест `namespace-install.yaml` того же тега, sha256 `baf0520c…3380d`) | `10-argocd.txt`, разделы 3–4 |
| `git` в образе ArgoCD | `2.51.0` | `10-argocd.txt`, раздел 2 |
| Dex (в составе ArgoCD) | `ghcr.io/dexidp/dex:v2.45.0` | `10-argocd.txt`, раздел 7 |
| Redis (в составе ArgoCD) | `docker.io/library/redis:8.2.3-alpine` — версия апстримная, реестр подменён | `10-argocd.txt`, раздел 6 |
| Argo Rollouts | `quay.io/argoproj/argo-rollouts:v1.9.1` (пин; релизные ассеты, install.yaml sha256 `78c82343…a40b5`, namespace-install.yaml sha256 `38eaadc3…0212b4`) | `50-rollouts.txt` |

Строка про боевой ArgoCD — для справки: стенд его только читает (командой
`kubectl -n argocd get deploy argocd-server -o jsonpath=…`) и своей версией на него не влияет.

## Изоляция и безопасность

Кластер k8s-volga — **общий и живой**: шесть узлов, на них Cilium, CoreDNS, CSI, cert-manager,
external-secrets, KEDA, vector, соседние стенды серии — и **боевой ArgoCD**, управляющий
реальными сервисами. Стенд про ArgoCD в кластере, где уже работает ArgoCD, — ровно тот случай,
где границы надо проводить явно.

**1. Свой namespace, а не общий.** Стенды фаз 1–3 живут в общем `cookbook-k8s`. Этот — в
`cookbook-argocd`, и на то две причины. Первая: сюда встанет отдельный ArgoCD со своим RBAC, и
в общем namespace он полез бы за объектами соседних стендов. Вторая: `teardown.sh` сносит
namespace **целиком** — так делать можно только со своим.

**2. Свой источник правды.** Git-сервер поднят внутри `cookbook-argocd`
(`git://git-server.cookbook-argocd.svc.cluster.local/demo.git`). Никаких внешних репозиториев,
токенов и deploy-ключей стенд не заводит и ни к каким чужим репозиториям не подключается.

**3. Боевой ArgoCD в namespace `argocd` — только чтение.** Стенд не создаёт, не меняет и не
удаляет в нём ничего. Контроль границ — замер **до и после** работы стенда одной и той же
командой, оба в фикстуре:

```
kubectl get applications.argoproj.io -A
No resources found                       # до M0
No resources found                       # после M0
No resources found                       # до M1
NS                NAME   SYNC     HEALTH # после M1 — единственный Application, и он наш
cookbook-argocd   demo   Synced   Healthy

kubectl -n argocd get applications.argoproj.io
No resources found in argocd namespace.  # после M1
```

Боевых `Application` в кластере **ноль** — и до стенда, и после. Боевой ArgoCD при этом
установлен и работает: семь подов в `argocd` с `RESTARTS 0` и возрастом пять суток,
`AppProject default` на месте (`generation: 1` — ни разу не переписан), — но управляемых
`Application` в нём сейчас нет. Поэтому критерий изоляции для M0–M4 формулируется как
**«было 0 — должно остаться 0»**.

**4. Чужие CRD не трогаются вовсе.** `applications.argoproj.io`,
`applicationsets.argoproj.io`, `appprojects.argoproj.io` и
`imageupdaters.argocd-image-updater.argoproj.io` принадлежат Helm-релизу боевого ArgoCD.
ArgoCD стенда **namespace-scoped** и переиспользует эти же CRD, своих не ставит: CRD в
Kubernetes объект кластерный, двух версий одного типа не бывает. Как это обеспечено на
практике — «Демо 1»; проверка после установки: тот же `creationTimestamp 2026-07-19T09:08:17Z`
и те же метки Helm. Удаление любого из них
снесло бы боевые `Application` вместе с определением типа — поэтому в `teardown.sh` список на
удаление задан **явными именами**, а не маской `*.argoproj.io`, и защищён предохранителем,
который останавливает скрипт, если чужое имя случайно попадёт в этот список.

**5. Control plane, узлы, kube-system — только чтение.** Тейнтов, `drain`/`cordon` и меток на
узлах стенд не ставит.

**6. Namespace под `pod-security.kubernetes.io/enforce: baseline`** — как и соседние
`cookbook-k8s`, `cookbook-net`. `restricted` не ставим: поды ArgoCD v3.x его не проходят без
правки чужого `securityContext`. Сам git-сервер, для сравнения, `restricted` проходит
(`runAsNonRoot`, `allowPrivilegeEscalation: false`, `drop: ["ALL"]`,
`seccompProfile: RuntimeDefault`). Зачем метка вообще понадобилась — следующий раздел.

**7. К git-серверу пускаются только поды своего namespace** — `NetworkPolicy`
`git-server-allow-stand-only` (9418/TCP, `namespaceSelector` по `cookbook-argocd`).
Тоже следующий раздел.

### Анонимный push + автосинхронизация: цепочка, закрытая до M2

Пункты 6 и 7 появились не для порядка, а после разбора одной цепочки — и это, пожалуй,
самое полезное, что стенд успел показать до перехода к automated sync.

Git-сервер стенда поднят с `--enable=receive-pack`, а у протокола `git://` нет ни
аутентификации, ни авторизации: пуш принимается от любого, кто дотянулся до порта 9418.
Дотянуться до M1 мог **любой под кластера** — семь `NetworkPolicy` из `namespace-install`
селектят только поды самого ArgoCD (`app.kubernetes.io/name=argocd-*`), git-сервер не попадал
ни под одну, а под, не выбранный ни одной политикой, в Kubernetes открыт всем.

Дальше складывается так:

```
любой под кластера → push в наш git → манифест с hostPath или privileged
                   → ArgoCD применяет его сам → под с доступом к узлу
```

В M1 цепочка рвётся посередине: `syncPolicy` ручной, между чужим коммитом и подом стоит
человек. В M2 включаются `automated` и `selfHeal` — и не рвётся уже нигде. Причём ArgoCD
здесь ни при чём: он честно делает ровно то, что ему поручено, — приводит кластер к
состоянию из git. **Источник правды GitOps — это право на запись в кластер, отложенное во
времени.** Права на репозиторий и права в кластере — одна и та же величина, просто вторая
проявляется позже.

Ограничение прав ArgoCD одним namespace (`11-rbac.yaml`) эту цепочку не рвёт: `hostPath` и
`privileged` — выход **за** namespace, на узел, и namespace-граница их не держит. Держит
Pod Security.

Поэтому замка два, и они на разных звеньях:

- **`NetworkPolicy`** решает, **кто может запушить**. Проверено обоими направлениями: клон
  из `cookbook-argocd` — 0.10 s, `rc=0`; клон из `cookbook-k8s` — 30 s тишины и `rc=15` по
  таймауту. Датаплейн здесь Cilium, поэтому «политика создана» и «политика работает» —
  проверялись раздельно.
- **Метка `baseline`** решает, **что будет, если всё-таки запушил**. Под с `hostPath: /` и
  `privileged: true` в этом namespace теперь не создаётся вовсе:

  ```
  Error from server (Forbidden): pods "psa-probe" is forbidden:
  violates PodSecurity "baseline:latest": hostPath volumes (volume "hostroot"),
  privileged (container "probe" must not set securityContext.privileged=true)
  ```

  Отказ приходит от admission — значит, придёт и контроллеру ArgoCD, когда тот попробует
  применить такой манифест из git.

Метка навешана на namespace, где уже работали девять подов: серверный `--dry-run=server`
предупреждений не выдал, все девять остались `Running` с нулём рестартов. Все замеры —
в `fixtures/30-hardening.txt`.

Для читателя, у которого репозиторий внешний, вывод переносится буквально: доступ на запись
в репозиторий, с которого синхронизируется кластер, стоит считать доступом в кластер — со
всеми вытекающими про ревью, защиту веток и подпись коммитов.

## Почему git-сервер внутри кластера

Источник правды в GitOps по определению внешний: репозиторий на хостинге, доступ по токену или
ключу, история изменений вне кластера. Стенд делает наоборот — и это осознанное упрощение, а
не рекомендация.

Причина одна: **воспроизводимость**. Стенд должен разворачиваться читателем целиком, одной
командой, без аккаунта на хостинге, без токена, без deploy-ключа и без «создайте у себя репу
и подставьте её URL в трёх местах». Всё, что нужно демо, — репозиторий, который отдаёт два
YAML-файла и принимает push. Такой репозиторий помещается в один манифест: `ConfigMap` с
содержимым, init-контейнер, который делает из него bare-репозиторий, и `git daemon` на 9418.

Что при этом теряется по сравнению с продом — и о чём стоит помнить, читая замеры:

- **Аутентификации нет.** Протокол `git://` анонимный, а `--enable=receive-pack` разрешает
  анонимный push. Внутри `ClusterIP` в учебном namespace это приемлемо; наружу такой сервер
  не выставляется никогда.
- **История не переживает под.** Репозиторий лежит в `emptyDir`: пересоздание пода означает
  чистый seed-коммит, все правки поверх него теряются. Для стенда это скорее плюс — состояние
  всегда известное. Хеш самого seed-коммита при этом **не меняется**, см. следующий раздел.
- **Ни webhook-ов, ни PR-flow.** Всё, что в проде даёт ревью и подпись коммитов, здесь
  отсутствует. Демо про sync и self-heal это не мешает — они смотрят на состояние ветки, а не
  на то, как коммит в неё попал.

### Seed-коммит воспроизводим: один и тот же хеш всегда

Репозиторий в `emptyDir` пересоздаётся при каждом старте пода, и наивная реализация даёт при
этом **новый хеш** seed-коммита. Для стенда это неприемлемо: на этот хеш ссылаются замеры
M1/M2/M4 — `revision` в статусе `Application`, diff при self-heal, точка отката после prune.
После любого `rollout restart` фикстуры превращались бы в вымысел.

Хеш коммита в git — SHA-1 от заголовка объекта: дерево, родители, автор с датой, коммиттер с
датой, сообщение. В seed-коммите всё, кроме дат, уже зафиксировано: содержимое лежит в
`ConfigMap`, личность задана `git config`, сообщение — константа. Недетерминизм давали
**только метки времени** — без них git берёт стенные часы. Контрольный замер (фикстура, блок
«воспроизводимость seed-коммита»): четыре независимых `git init` + `commit` с одним и тем же
содержимым, первые два — без переменных, вторые два — с ними:

```
# без GIT_AUTHOR_DATE/GIT_COMMITTER_DATE — два разных хеша
4a992025e3725adffb927bf1b73b94968a56f7b5
3aeaacb63ac899e37240dc3db40d47b003059a6d
# с ними — один и тот же
3a1433da0d9e43e0f3e7e454497a14b775ab92c9
3a1433da0d9e43e0f3e7e454497a14b775ab92c9
```

(Эти четыре хеша — от минимального однофайлового репозитория в поде-пробе; они показывают
механизм, а не хеш самого стенда.)

Отсюда фикс: init-контейнер `seed-repo` получает `GIT_AUTHOR_DATE` и `GIT_COMMITTER_DATE`,
равные `2026-07-24T12:00:00+00:00`. Seed-коммит стенда — всегда
`d049270d788de26273d85992c271c5994a6d7ca8`, и это проверено на самом стенде: под удалён,
Deployment поднял новый (другое имя, другой узел, пустой том), `git ls-remote` до и после
возвращает один и тот же хеш.

Хеш зависит от содержимого `ConfigMap git-seed`, сообщения коммита и личности коммиттера — всё
это в манифесте зафиксировано. Правка любого из трёх меняет seed-хеш; тогда его надо
переснять и обновить в фикстурах M1/M2/M4.

**PVC для этого не нужен и не заводится** — и это отдельное решение, а не экономия. Во-первых,
единственный `StorageClass` кластера — `nfs-csi` с `reclaimPolicy: Retain` (и он не дефолтный):
после `teardown.sh` остался бы `PV` в состоянии `Released`, который надо убирать руками, — то
есть стенд перестал бы убираться за собой. Во-вторых, переживший teardown том протащил бы
мутации демо M2 (ручная правка, prune) в следующий прогон, и «чистый старт» стал бы
недостижим. Воспроизводимость решается двумя переменными окружения, а не томом.

### Про образ: `git daemon` есть далеко не везде

Первым кандидатом был `alpine/git` — и он не подошёл. Замеры из фикстуры, блок «поиск образа»:

| Образ | git | `git daemon` |
|---|---|---|
| `alpine/git:v2.49.1` | 2.49.1 | нет: `git: 'daemon' is not a git command` |
| `registry.k8s.io/git-sync/git-sync:v4.4.0` | 2.39.5 | нет, та же ошибка |
| `bitnami/git:2.49.0` | — | образа больше нет: `docker.io/bitnami/git:2.49.0: not found` |
| `buildpack-deps:bookworm-scm` | 2.39.5 | **есть**: `/usr/lib/git-core/git-daemon` |

Причина у первых двух общая: в Alpine `git-daemon` вынесен в отдельный пакет, а образы на
Debian-базе часто собирают git без него. Доставить пакет на старте пода технически можно —
`apk add --no-cache git-daemon` из кластера проходит, вывод в фикстуре:

```
fetch https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64/APKINDEX.tar.gz
fetch https://dl-cdn.alpinelinux.org/alpine/v3.22/community/x86_64/APKINDEX.tar.gz
(1/1) Installing git-daemon (2.49.1-r0)
OK: 88 MiB in 67 packages
usage: git daemon [--verbose] [--syslog] [--export-all]
           [--timeout=<n>] [--init-timeout=<n>] [--max-connections=<n>]
           …                       # полный usage — в фикстуре
rc=129
```

После установки `git daemon` печатает usage и выходит с кодом 129 (флага `--version` у него
нет) — вместо прежнего `git: 'daemon' is not a git command`. Но это поход в сеть за пакетами
при **каждом** старте пода — для стенда про герметичность так себе решение.

Выбран `buildpack-deps:bookworm-scm`, запинованный **по дайджесту**, а не по тегу: `bookworm-scm`
плавающий, а стенд про воспроизводимость. Тем же образом работают и init-контейнер, и демон —
второй образ в манифест не тянется.

Отдельно: пробы у демона — `exec` с `git ls-remote`, а не `tcpSocket`. `tcpSocket` открывает и
рвёт соединение, и `git daemon` на каждую такую пробу пишет `fatal: the remote end hung up
unexpectedly`; за час лог сервера превращается в мусор. `git ls-remote` проходит протокол
целиком и заодно проверяет, что репозиторий реально отдаётся, а не что порт просто открыт.

## Демо

| # | Демо | Задача | Фикстура |
|---|---|---|---|
| 0 | Каркас: свой namespace, квота, git-сервер в кластере | M0 | `00-git-server.txt` |
| 1 | ArgoCD стенда, `Application`, первый sync | M1 | `10-argocd.txt`, `20-application.txt` |
| 2 | Расхождение: ручная правка, self-heal, prune | M2 | `30-selfheal.txt` |
| 3 | app-of-apps: дерево приложений и удаление корневого | M3 | `40-app-of-apps.txt` |
| 4 | Progressive delivery: canary c анализом метрик и автооткатом, blue-green (Argo Rollouts) | M4 | `50-rollouts.txt`, `60-canary.txt`, `70-rollback.txt`, `80-bluegreen.txt` |

## Демо 0: каркас и герметичный источник правды

Фикстура: `fixtures/00-git-server.txt`.

```bash
export KUBECONFIG=/path/to/k8s-volga/kubeconfig   # контекст admin@k8s-volga
bash apply.sh manifests/00-namespace.yaml
bash apply.sh manifests/05-git-server.yaml
kubectl -n cookbook-argocd rollout status deploy/git-server --timeout=180s
```

Проверка, что репозиторий действительно отдаётся и принимает (а не что под просто `Running`) —
временный под изнутри кластера. Скрипт делает всё разом: клонирует, показывает историю и
дерево, пушит проверочный коммит (`receive-pack` понадобится в M2 для демо prune) и **откатывает
push обратно**, чтобы репозиторий остался на seed-коммите:

```bash
kubectl -n cookbook-argocd run git-probe --image=alpine/git:v2.49.1 --restart=Never --command -- sh -c '
  set -e
  echo "=== git clone git://git-server.cookbook-argocd.svc.cluster.local/demo.git ==="
  git clone git://git-server.cookbook-argocd.svc.cluster.local/demo.git /tmp/demo
  cd /tmp/demo
  echo "=== git log --oneline ==="
  git log --oneline
  echo "=== ls -R ==="
  ls -R
  echo "=== содержимое apps/demo/deployment.yaml ==="
  cat apps/demo/deployment.yaml
  echo "=== проверка receive-pack (в M2 понадобится push) ==="
  git config user.email "probe@khorost.tech"
  git config user.name "git-probe"
  printf "\n# probe: временная строка для проверки push\n" >> apps/demo/service.yaml
  git commit -q -am "probe: проверка receive-pack"
  git push origin main
  echo "=== ветки на сервере после push ==="
  git ls-remote origin
  echo "=== откат: возвращаем репозиторий в исходное состояние ==="
  git push --force origin HEAD~1:main
  git ls-remote origin'
kubectl -n cookbook-argocd logs git-probe
kubectl -n cookbook-argocd delete pod git-probe
```

В фикстуре — полный вывод: клон проходит, в репозитории один seed-коммит
`d049270 seed: demo Deployment и Service`, дерево `apps/demo/{deployment.yaml,service.yaml}`,
push принят (`d049270..ed5ab28  main -> main`), откат выполнен
(`+ ed5ab28...d049270 HEAD~1 -> main (forced update)`), и финальный `ls-remote` снова
показывает `d049270…` на `HEAD` и `refs/heads/main`.

Проверка воспроизводимости seed-хеша (то же `ls-remote` до и после пересоздания пода):

```bash
kubectl -n cookbook-argocd run seedhash-1 --image=alpine/git:v2.49.1 --restart=Never --command -- \
  sh -c 'git ls-remote git://git-server.cookbook-argocd.svc.cluster.local/demo.git'
kubectl -n cookbook-argocd logs seedhash-1

kubectl -n cookbook-argocd delete pod -l app=git-server --wait=true
kubectl -n cookbook-argocd rollout status deploy/git-server --timeout=180s

kubectl -n cookbook-argocd run seedhash-2 --image=alpine/git:v2.49.1 --restart=Never --command -- \
  sh -c 'git ls-remote git://git-server.cookbook-argocd.svc.cluster.local/demo.git'
kubectl -n cookbook-argocd logs seedhash-2
```

Обе команды печатают `d049270d788de26273d85992c271c5994a6d7ca8` — при том что под новый и
приехал на другой узел. Временные поды после снятия вывода удаляются
(`kubectl -n cookbook-argocd delete pod seedhash-1 seedhash-2 git-probe`).

Что стенд занимает на старте, по данным квоты: `requests.cpu 20m`, `requests.memory 32Mi`,
один под. Потолок `limits.cpu` учитывает init-контейнер (квота берёт максимум между суммой
обычных контейнеров и максимумом init-контейнеров), поэтому в `Used` видно `500m`, а не `200m`.

## Демо 1: свой ArgoCD и первый Application

Фикстуры: `fixtures/10-argocd.txt` (установка и границы), `fixtures/20-application.txt` (цикл
sync), `fixtures/30-hardening.txt` (закрытие namespace и git-сервера, сужение прав сервера).

```bash
export KUBECONFIG=/path/to/k8s-volga/kubeconfig   # контекст admin@k8s-volga
bash manifests/10-argocd.sh                       # скачать, отфильтровать, применить
bash apply.sh manifests/11-rbac.yaml              # права на управление своим namespace
bash apply.sh manifests/15-cluster-scope.yaml     # ограничить область наблюдения
bash apply.sh manifests/20-application.yaml       # первый Application, ручной режим
```

### Почему установка без CRD и с пином версии

Это свойство **этого** кластера, а не рекомендация. Обычному читателю на своём кластере
подходит штатный `install.yaml` без всяких фильтров.

Здесь ситуация другая: k8s-volga общий, в нём уже работает боевой ArgoCD, а CRD
`applications.argoproj.io`, `applicationsets.argoproj.io`, `appprojects.argoproj.io` — объекты
**кластерного** уровня. Они одни на весь кластер, и у них есть хозяин:

```
NAME                          CREATED                MANAGED-BY   PART-OF   HELM-NS
applications.argoproj.io      2026-07-19T09:08:17Z   Helm         argocd    argocd
applicationsets.argoproj.io   2026-07-19T09:08:17Z   Helm         argocd    argocd
appprojects.argoproj.io       2026-07-19T09:08:17Z   Helm         argocd    argocd
```

`app.kubernetes.io/managed-by: Helm` и `meta.helm.sh/release-namespace: argocd` — это релиз
боевого ArgoCD. Применить манифест, содержащий те же CRD, значит перехватить их у Helm и,
возможно, подменить схему под боевыми `Application`. Отсюда три решения:

1. **`namespace-install.yaml` вместо `install.yaml`.** Разница между ними на теге `v3.4.1`
   измеряется, а не предполагается: `install.yaml` — 1 890 674 байта, три
   `CustomResourceDefinition` и три `ClusterRole`/`ClusterRoleBinding` на весь кластер.
   Роли при этом разные по ширине, и это стоит знать точно (разбор файла —
   в `fixtures/10-argocd.txt`, раздел 3): звёздочка по всем трём осям
   `["*"]/["*"]/["*"]` там ровно ОДНА — у `argocd-application-controller`, и это буквально
   админ кластера (плюс второе правило `nonResourceURLs: ["*"]`). У `argocd-server` тоже
   `["*"]/["*"]`, но глаголами `delete,get,patch` — без `create` и `update`, дальше узкие
   правила (`pods/log` get, `jobs` create, `events` list). У
   `argocd-applicationset-controller` звёздочек нет вовсе. Одной тотальной роли, впрочем,
   более чем достаточно: `install.yaml` в общем кластере — это админ на весь кластер плюс
   перехват трёх чужих CRD. `namespace-install.yaml` — 88 049 байт, ни одного CRD и
   ни одной кластерной роли. Почти весь объём `install.yaml` — это схемы CRD.
2. **Пин `v3.4.1`** — ровно версия боевого (`quay.io/argoproj/argocd:v3.4.1`). CRD общие, читать
   их будут два контроллера сразу; разные версии означали бы расчёт на разные схемы.
3. **Фильтр `kind: CustomResourceDefinition` всё равно остаётся в скрипте**, хотя на `v3.4.1`
   он вырезает ноль документов:

   ```
   документов в исходном манифесте: 50
   вырезано kind: CustomResourceDefinition: 0 (имён нет)
   осталось к применению: 50
   ПРОВЕРКА: CRD в применяемом манифесте = 0
   ПРОВЕРКА: ClusterRole/ClusterRoleBinding в применяемом манифесте = 0
   ```

   Смысл фильтра не в перестраховке, а в том, что «CRD не применялись» превращается из
   обещания в число в выводе. И если в другой версии CRD в этот манифест вернутся, установка
   сломается громко, а не тихо перехватит чужое.

Предполётная проверка перед всем этим отвечала на один вопрос: не смотрит ли боевой
контроллер в чужие namespace. Ключ `application.namespaces` в его `argocd-cmd-params-cm`
не задан, флагом не передаётся (`grep -c 'application-namespaces'` по подам `argocd` → `0`).
Если бы он был задан со звёздочкой или с нашим namespace — ставить второй ArgoCD было бы
нельзя, боевой контроллер начал бы управлять нашими `Application`.

### Namespace-scoped — это два решения, а не одно

Штатный `namespace-install.yaml` даёт только первое: RBAC ограничен `Role`/`RoleBinding` в
своём namespace. Установка при этом запускается — и не работает. Первый `Application` встаёт
в `Unknown`:

```
Failed to load live state: ... failed to sync cluster https://10.96.0.1:443: failed to load
initial state of resource ClusterTriggerAuthentication.keda.sh: failed to list resources:
clustertriggerauthentications.keda.sh is forbidden: User
"system:serviceaccount:cookbook-argocd:argocd-application-controller" cannot list resource
"clustertriggerauthentications" in API group "keda.sh" at the cluster scope
```

Контроллер по умолчанию считает себя хозяином кластера и на старте строит кэш всех типов,
включая кластерные. В общем кластере он спотыкается о первый же чужой CRD. Лечится это не
правами (права ровно такие, какие заказывали), а описанием кластера —
`manifests/15-cluster-scope.yaml` задаёт `namespaces: cookbook-argocd` и
`clusterResources: "false"`.

Второе, что выясняется сразу за этим: `Role` из `namespace-install` не даёт контроллеру права
создать вообще ничего, кроме своих же CRD —

```
[""]            secrets, configmaps                          get,list,watch
["argoproj.io"] applications, applicationsets, appprojects    create,get,list,watch,update,patch,delete
[""]            events                                       create,list
["apps"]        deployments                                  get,list,watch
```

В обычной установке всё остальное даёт `ClusterRole` с `["*"]/["*"]/["*"]`. Мы её ставить не
имеем права, поэтому `manifests/11-rbac.yaml` выдаёт те же полномочия **Ролью в одном
namespace**. Да, со звёздочками — GitOps-контроллер обязан уметь создавать произвольные типы,
состав манифестов в git заранее неизвестен, а cluster cache всё равно перечисляет все
namespaced-типы своей области. Граница здесь проходит не по глаголам, а по namespace, и она
проверяемая:

```
can-i list deployments  -n cookbook-argocd : yes
can-i create deployments -n cookbook-argocd: yes
can-i list deployments  -n argocd          : no
can-i list deployments  -n kube-system     : no
can-i list nodes (кластерный ресурс)       : no
can-i list applications -n argocd          : no
```

Всё вышесказанное — про **контроллер**. Второй ServiceAccount, `argocd-server`, сначала был
привязан к той же широкой роли, и это оказалось перебором: апстримная `ClusterRole`
`argocd-server` даёт по `*/*` не всё, а ровно `delete,get,patch` — без `create` и `update`, —
плюс несколько узких правил (`pods/log` get, `jobs` create, `events` list). Причина в
устройстве ArgoCD: живое состояние сервер берёт из кэша контроллера, а к API ходит только за
действиями из UI и CLI — удалить ресурс, показать лог пода, перезапустить `Deployment`
(перезапуск — это `patch` аннотации), запустить `Job` из ресурс-хука. Поэтому у него теперь
своя `Role` `cookbook-argocd-server-ns` с апстримным набором:

```
                       controller   server
create deployments   : yes          no
update deployments   : yes          no
delete deployments   : yes          yes
get    deployments   : yes          yes
patch  deployments   : yes          yes
list   deployments   : yes          no
```

Работоспособность проверена не только на `can-i`: `argocd app get`, `argocd app logs` и
`argocd app actions run demo restart` через сервер проходят, `Application` остаётся
`Synced/Healthy`, `forbidden` в логах — ноль (`fixtures/30-hardening.txt`). Практический
вывод для статьи: «GitOps-контроллеру нужна звёздочка» верно ровно для **одного** компонента
из трёх, и это стоит знать, прежде чем выдавать звёздочку всем трём.

Кластерных ролей с именем `argocd` в кластере ровно восемь — те же, что были до установки,
все с `creationTimestamp 2026-07-19`, и все их `subjects` смотрят в ns `argocd`. Наших
`ServiceAccount` из `cookbook-argocd` нет ни в одной кластерной привязке.

### Квота: почему потолок пришлось поднять

Первая редакция квоты (`limits.cpu 4`) не пустила седьмой компонент ArgoCD:

```
Error creating: pods "argocd-repo-server-6b75cff94-jcltq" is forbidden: exceeded quota:
cookbook-gitops-quota, requested: limits.cpu=500m, used: limits.cpu=4, limited: limits.cpu=4
```

Причина не в аппетите ArgoCD. В его манифестах `resources` пустые
(`kubectl get deploy argocd-repo-server -o jsonpath='{…resources}'` → `{}`), поэтому limits
проставляет `LimitRange` стенда — по `500m`/`512Mi` каждому поду, независимо от реального
потребления. Квота на `limits` в такой конфигурации считает не нагрузку, а **штуки подов**:
`4 CPU / 500m = 8`. Восемь и было занято, при том что `requests` стояли на `370m` из `2` CPU,
а потолок `pods: 25` разрешал двадцать пять — два ограничителя противоречили друг другу.

Поднято осознанно и до внутренней непротиворечивости: `limits.cpu 8`, `limits.memory 8Gi`,
`pods 16` (восемь CPU по `500m` — это ровно шестнадцать подов). Квота **не отключена**;
`requests` не менялись (`2` CPU / `3Gi`) — именно они остаются настоящим ограничителем
нагрузки на узлы. Разрыв между потолком и реальностью виден в цифрах: `limits.cpu Used 4500m`
против `14m` фактического потребления всех девяти подов стенда.

### git:// работает — и это проверено до, а не после

Схему `git://` ArgoCD принимает. Проверялось тремя независимыми способами, все в фикстуре:
`git ls-remote` из образа `quay.io/argoproj/argocd:v3.4.1` (транспорт есть, хотя
`git-remote-git` в списке помощников отсутствует — он вкомпилирован), `argocd admin repo
generate-spec git://…` (URL принят как есть, `type: git`), и наконец сам `argocd-repo-server`
уже после установки. Переделывать git-сервер на smart HTTP не потребовалось.

### Первый цикл

`Application` создаётся **без `syncPolicy`** — ручной режим. ArgoCD читает репозиторий,
видит расхождение и не делает ничего:

```
NAME   SYNC STATUS   HEALTH STATUS
demo   OutOfSync     Missing

sync.revision = d049270d788de26273d85992c271c5994a6d7ca8
/v1      Service/demo    -> OutOfSync
apps/v1  Deployment/demo -> OutOfSync
```

Хеш он знает уже здесь — это seed-коммит из M0. Sync запускается без локального CLI, правкой
поля `operation` (то же самое делает кнопка Sync в UI):

```bash
kubectl -n cookbook-argocd patch application demo --type merge \
  -p '{"operation":{"initiatedBy":{"username":"stand"},"sync":{"revision":"HEAD","syncStrategy":{"apply":{}}}}}'
```

Результат — за секунду (`startedAt 16:14:56` → `finishedAt 16:14:57`):

```
NAME   SYNC STATUS   HEALTH STATUS
demo   Synced        Healthy

operation.phase   = Succeeded
operation.message = successfully synced (all tasks run)
operation.revision = HEAD                                        <- о чём просили
synced revision    = d049270d788de26273d85992c271c5994a6d7ca8    <- что применилось
Service/demo     status=Synced  message=service/demo created
Deployment/demo  status=Synced  message=deployment.apps/demo created
```

`revision` совпадает с seed-коммитом, который отдаёт наш git-сервер (`git ls-remote` в
фикстуре) — это доказательство, что источником правды был именно он. Namespace объектам никто
не прописывал: в git его нет, подставил `destination.namespace`. Принадлежность ArgoCD
записывает аннотацией `argocd.argoproj.io/tracking-id: demo:apps/Deployment:cookbook-argocd/demo`,
метки на объекте — только те, что лежат в репозитории.

Границы после всего: `Application` в ns `argocd` по-прежнему **ноль**, CRD с прежним
`creationTimestamp 2026-07-19` и метками Helm, боевые Deployment на месте, его
`AppProject default` — `generation: 1`, то есть ни разу не переписан. `AppProject default` в
нашем namespace — отдельный объект, его создал **наш** `argocd-server` при первом старте
(`2026-07-24T16:00:46Z` против `2026-07-19T09:08:29Z` у боевого).

### Идемпотентность

Третий прогон `10-argocd.sh` подряд: `48 unchanged`, `2 configured`. Два `NetworkPolicy`
показывают `configured` каждый раз, и это не изменение объекта — в манифесте порт `8084`
объявлен без `protocol`, а API-сервер проставляет ему `TCP` по умолчанию, из-за чего `kubectl`
каждый раз считает патч. `generation` и `resourceVersion` при этом не двигаются
(`1 -> 1`, `2256819 -> 2256819`), поды — без единого рестарта.

## Демо 2: self-heal, дрейф и prune

Фикстура: `fixtures/30-selfheal.txt`.

Первый sync — самое скучное, что есть в GitOps. Интересное начинается после: кластер живёт,
его правят руками, из репозитория удаляют файлы. Демо отвечает на три вопроса — и отвечает
**замерами времени, а не пересказом документации**. Все три числа ниже сняты с отметками
`date -u` на живом стенде; ни одно не взято из документации ArgoCD.

Режим включается правкой `manifests/20-application.yaml` — то есть тоже декларативно:

```yaml
syncPolicy:
  automated:
    prune: true
    selfHeal: true
```

### Фактические таймауты этого экземпляра

`timeout.reconciliation` в `argocd-cm` нашего namespace **не задан** — как и весь
`argocd-cmd-params-cm` (`DATA 0`). Сверяться с документацией здесь нельзя: значение надо снять.
Контроллер читает эти ключи через `configMapKeyRef` с `optional: true`, поэтому в поде
соответствующих переменных окружения нет вовсе (`env | grep -Ei "RECONCIL|SELF_HEAL"` — пусто),
а значения по умолчанию печатает сам бинарник в этом же поде и его же лог при старте:

```
appResyncPeriod=2m0s, appHardResyncPeriod=0s, appResyncJitter=1m0s
```

| Параметр | Ключ в ConfigMap | Фактически |
|---|---|---|
| интервал опроса репозитория | `timeout.reconciliation` | не задан → **2m0s** |
| джиттер к нему | `timeout.reconciliation.jitter` | не задан → **1m0s** |
| «жёсткий» ресинк | `timeout.hard.reconciliation` | не задан → **0s**, выключен |
| фиксированная пауза self-heal | `controller.self.heal.timeout.seconds` | не задан → **0**, отключена |
| база паузы backoff | `controller.self.heal.backoff.timeout.seconds` | **2 s** |
| множитель backoff | `controller.self.heal.backoff.factor` | **3** |
| потолок backoff | `controller.self.heal.backoff.cap.seconds` | **300 s** |
| таймаут sync | `controller.sync.timeout.seconds` | **0**, sync не прерывается |

Правило backoff, которое из этих трёх чисел складывается, звучит так: **после попытки
self-heal номер N контроллер выдерживает `2 × 3^(N−1)` секунд (но не больше 300) и только
потом разрешает попытку N+1**. Перед первой попыткой паузы нет — отсчитывать не от чего.
Правило не выведено из документации: контроллер сам печатает остаток текущей паузы строками
`retrying in ...`, и все девять таких строк за прогон лежат в фикстуре целиком.

Ключевое, что стоит унести отсюда: **опрос репозитория и реакция на дрейф — разные пути**.
Расхождение внутри кластера контроллер видит через watch и реагирует за секунды. Новую
ревизию в git — только по опросу. Отсюда и разница в замерах: 1.6 s против ~120.6 s.

### Замер 1: дрейф, который ArgoCD чинит — 1.6 s, а потом 54.4 s

В git у `Deployment demo` стоит `replicas: 1`. Правим руками на 3:

```
17:07:21      (часы кластера) плановый опрос: comparison expired → Skipping auto-sync: Synced
17:07:27.775  (часы хоста)    kubectl scale deploy/demo --replicas=3
17:07:29      (часы кластера) Refreshing app status (controller refresh requested), level (1)
17:07:29      (часы кластера) Initiated automated sync to 'd049270d…', SelfHealAttemptsCount:1
17:07:29      (часы кластера) Applying resource Deployment/demo → deployment.apps/demo configured
                              sync/terminate complete: duration 485507072 ns = 0.486 s
17:07:29.349  (часы хоста)    первое наблюдение spec.replicas=1
```

`17:07:29.349 − 17:07:27.775 = 1.574 s` — **1.6 секунды**, обе отметки хостовые, вычитание
сырое. Это верхняя граница: цикл наблюдения ходит раз в ~1.2 s, а сам `apply` по логу занял
0.486 s. И заметьте, ревизия та же самая, `d049270d…`: синхронизацию вызвал дрейф, а не коммит.

Плановый цикл опроса тут ни при чём: он отработал незадолго до правки и написал «Skipping
auto-sync: application status is Synced». «Незадолго» — это **~8.3 s**, и вот тут нужна
поправка на часы (см. ниже): опрос в `17:07:21` по часам кластера — это `17:07:19.47` по часам
хоста, а правка сделана в `17:07:27.775` по хосту. Вычесть одно из другого напрямую нельзя.

Паузы перед этой попыткой не было и быть не могло: backoff отсчитывается **от предыдущей
попытки**, а предыдущей не существовало (`SelfHealAttemptsCount:1`). Пауза назначается **после**
попытки и придерживает следующую. Видно это на дрейфах подряд:

| попытка | требовалось выждать | прошло с прошлой попытки | замер возврата |
|---|---|---|---|
| 1 | ничего: предыдущей попытки нет | — | **1.6 s** |
| 2 | 2 s — пауза после попытки 1 | 107 s | **1.4 s** |
| 3 | 6 s — пауза после попытки 2 | 318 s | **1.4 s** |
| 4 | 18 s — пауза после попытки 3 | 66 s | **1.6 s** |
| 5 | 54 s — пауза после попытки 4 | 55 s | **54.4 s** ← ждали по-настоящему |
| 6 | 162 s — пауза после попытки 5 | 162 s | **35.8 s** ← правка попала в середину паузы |

Пятый дрейф запускался через 0.1 s после того, как отработал self-heal четвёртого, — и
контроллер честно написал, чего он ждёт. Вот весь вывод `grep 'retrying in'` за прогон,
девять строк, сжатый до «остаток паузы + время»:

```
retrying in 5.314861668s      17:09:16    ← остаток паузы после попытки 2 (полная 6 s)
retrying in 17.182684415s     17:14:34    ← после попытки 3 (полная 18 s)
retrying in 53.639418159s     17:15:41    ← после попытки 4 (полная 54 s)
retrying in 52.455927351s     17:15:42
retrying in 51.857316418s     17:15:43
retrying in 2m41.493841217s   17:16:35    ← после попытки 5 (полная 162 s)
retrying in 33.35495625s      17:18:43    ← её же остаток
retrying in 32.653670262s     17:18:44
retrying in 4m59.40073637s    17:19:17    ← после попытки 6: формула требует 486 s,
                                            напечатано 299.4 s — сработал потолок 300 s
```

Последняя строка ценнее остальных: **потолок `--self-heal-backoff-cap-seconds = 300` не взят из
`--help`, а показан в работе**. `2 × 3⁵ = 486 s`, а контроллер напечатал `4m59.4s`.

Каждая пауза ложится на `2 × 3^(N−1)` без натяжек: `5.31 < 6`, `17.18 < 18`, `53.64 < 54`,
`2m41.49 = 161.49 < 162` — строка печатается через доли секунды после начала паузы, поэтому
остаток чуть меньше полного значения. Шестой дрейф подтвердил 162 s независимо: попытка №5
пришлась на 17:16:35, `17:16:35 + 162 s = 17:19:17`, и попытка №6 в логе стоит ровно на
17:19:17 — там же вернулись `replicas`.

**Вывод.** «ArgoCD откатывает правку за пару секунд» верно ровно один раз. Если один и тот же
объект правят раз за разом — руками или другим контроллером, — окно, в котором кластер живёт с
чужим состоянием, растёт как 2 → 6 → 18 → 54 → 162 s и упирается в потолок 300 s. Пять минут
расхождения self-heal уже не назвать.

### Замер 2: дрейф, который ArgoCD НЕ чинит

Три правки делаются одновременно, чтобы разница была видна в **одном и том же** проходе
self-heal:

| что изменили | что стало |
|---|---|
| `spec.replicas` 1 → 2 — поле **объявлено** в git | откатилось |
| метка `drift-probe=manual` на `Deployment demo` — объект свой, поля в git нет | **осталась** |
| аннотация `drift-probe=manual` там же | **осталась** |
| `ConfigMap manual-not-in-git`, созданный руками рядом | **остался**, хотя `prune: true` |

При этом `Application` показывает `Synced`. То есть diff у ArgoCD — не «объект в кластере равен
объекту в git», а «всё **объявленное** в git присутствует в кластере в этом виде». Лишнее в
живом объекте в сравнение не входит.

Владение видно и по аннотациям, и по статусу самого `Application`:

```
Deployment/demo  argocd.argoproj.io/tracking-id = demo:apps/Deployment:cookbook-argocd/demo
Service/demo     argocd.argoproj.io/tracking-id = demo:/Service:cookbook-argocd/demo
ConfigMap/manual-not-in-git   — аннотаций нет вовсе

status.resources: /Service demo Synced, apps/Deployment demo Synced   ← ровно два объекта
```

`prune` удаляет только то, что **было** в git и оттуда пропало. «Не было никогда» — это другое.

**Вывод, и он важнее, чем кажется.** Ровно на этом свойстве держится совместимость с чужими
контроллерами: HPA правит `replicas`, service mesh дописывает аннотации, admission-вебхуки
навешивают метки — всё выживает, потому что в git не объявлено. И ровно здесь же расходятся
ожидания: «GitOps гарантирует, что в кластере то же, что в git» — неверно. Гарантия одна: в
кластере **есть** то, что в git. Чего в git нет — не гарантируется ничего, включая целые
объекты в том же namespace.

### Замер 3: prune — ~120.6 s, из которых работы на полсекунды

Коммит, удаляющий `apps/demo/service.yaml`, делается **из пода внутри `cookbook-argocd`**
(`git-push.sh`). Не потому что так удобнее, а потому что `NetworkPolicy`
`git-server-allow-stand-only` пускает к порту 9418 только поды этого namespace, и ослаблять её
ради демо нельзя — это один из двух замков, закрытых в M1. Под проходит `baseline` честно:
`runAsNonRoot`, `drop: ["ALL"]`, `seccompProfile: RuntimeDefault`, без `hostPath`.

Даты коммита прибиты так же, как у seed-коммита, — иначе хеш в фикстуре был бы плавающим:

```
GIT_AUTHOR_DATE=GIT_COMMITTER_DATE=2026-07-24T13:00:00+00:00
коммит удаления: 41e857605e9bf396f20966470d0dea259c4336b4
```

```
17:21:24.905  push коммита 41e8576                                       (часы пода)
   …          контроллер в репозиторий не заглядывает
17:23:25      Refreshing app status (comparison expired, reconciledAt: 17:19:17, expiry: 2m0s)
17:23:25      Initiated automated sync to '41e857605e9bf396f20966470d0dea259c4336b4'
17:23:25      Service/demo → status 'Pruned', message 'pruned'           (часы кластера)
              Deployment/demo → 'unchanged'
              sync/terminate complete: duration 487411970 ns = 0.487 s
17:23:26.72   цикл ВПЕРВЫЕ видит, что Service/demo нет    (17:23:25.190 по часам хоста)
```

**~120.6 секунды**, из которых работы — 0.487 s. Остальные две минуты — ожидание опроса.

Числа тут два, и путать их не стоит — ровно как с замерами self-heal:

- **~120.6 s** — по логу: push в `17:21:24.905`, sync начат в `17:23:25` (это 120.1 s), сам
  sync занял 0.487 s. Лог печатает целые секунды, отсюда ±1 s;
- **121.8 s** — по наблюдению, и это **верхняя граница**: `17:23:26.72` — не момент удаления, а
  момент, когда цикл впервые на него посмотрел. Шаг цикла 3.5 s (предыдущая итерация —
  `17:23:21.6`), так что истинное значение лежит в 118.3…121.8 s.

И вот тут нельзя останавливаться на «значит, задержка равна `timeout.reconciliation`». Это
неверно дважды. Во-первых, коммит попадает в случайную точку цикла: ждать можно от нуля до
полной длины цикла. Во-вторых, фактическая длина цикла у ненастроенного экземпляра — не
«2 минуты». Двадцать подряд идущих плановых сверок из того же лога:

```
+271 +192 +171 +225 +229 +259 +211 +125 +146 +192
+403 +243 +217 +132 +239 +259 +174 +249 +282   (секунды)
```

От 125 до 403 секунд при настройке `2m0s + джиттер 1m0s`. Часть разброса объясняется тем, что
таймер отсчитывается от **последней сверки любой природы** (в том числе вызванной self-heal), а
не от предыдущего планового опроса, — но и конкретный наш случай выходит за сумму:
`reconciledAt 17:19:17`, `expiry 2m0s`, сверка в 17:23:25, то есть через 248 s. Наши ~120.6 s —
удачный жребий, а не константа.

### Сколько ждали бы сами и сколько получилось с пинком

Возврат к seed делается тем же способом: `git reset --hard` до seed-коммита и `push --force` из
пода. Дальше — принудительный refresh, чтобы честно развести две величины:

```
17:24:17      последняя сверка ПЕРЕД push — за 64 s до него
17:25:21.281  force-push seed-ревизии; после него и до пинка контроллер в git не заглядывал
17:25:35.98   kubectl patch application demo -p '{"metadata":{"annotations":
                {"argocd.argoproj.io/refresh":"normal"}}}'
17:25:36      Refreshing app status (normal refresh requested)   ← реакция на аннотацию
17:25:36      Initiated automated sync to 'd049270d…' → service/demo created
              sync/terminate complete: duration 629023259 ns = 0.629 s
17:25:36.46   Service/demo снова в кластере
```

Сами — ждали бы до следующего планового опроса: по замеру выше это ~120.6 s, а по таблице длин
циклов — от 125 до 403 s. С пинком — меньше секунды. Пинок не ускоряет ArgoCD, он его будит; в
проде тот же эффект даёт webhook от хостинга репозитория. Без webhook GitOps работает опросом, и
цена опроса — вот эта.

### Про часы: почему замер prune чуть не оказался занижен

Часть отметок снята часами хоста (`kubectl` запускается с ноутбука), часть — часами кластера
(лог контроллера, `date` внутри пода). Прежде чем вычитать одно из другого, расхождение надо
измерить, а не предположить, что его нет:

```
cluster - host = +1.53 s   (три замера подряд, RTT 0.21 s)
```

Замеры возврата после дрейфа это не затрагивает — там **обе** отметки хостовые, сдвиг
сокращается: 1.6 / 1.4 / 1.4 / 1.6 / 54.4 s остаются как есть.

А вот там, где рядом с хостовой отметкой встаёт отметка **из лога**, поправка нужна, и её легко
забыть:

- замер prune смешивал часы пода и часы хоста: сырое вычитание давало 120.3 s, с поправкой —
  **121.8 s** (верхняя граница; по логу — ~120.6 s);
- «плановый опрос прошёл незадолго до правки»: опрос в `17:07:21` по кластеру = `17:07:19.47`
  по хосту, правка в `17:07:27.775` по хосту, разрыв **~8.3 s**. Без поправки получилось бы
  6.3 s — число, смешивающее двое часов;
- в замере возврата Service без поправки выходила бы прямая бессмыслица: объект наблюдался в
  17:25:34.925 по хосту, а контроллер записал `service/demo created` в 17:25:36 по кластеру —
  «появился раньше, чем создали». С поправкой всё сходится.

### Prune не бесплатен: ClusterIP другой

После удаления и возврата `Service demo` — это **другой** Service:

```
было   10.105.220.154
стало  10.97.138.219
```

Удалить манифест из git и вернуть обратно — это не «ничего не изменилось». Для `Service` это
потерянный `ClusterIP` (и все, кто закешировал адрес вместо DNS-имени), для `PVC` это был бы
потерянный том, для `Secret` — пересозданный секрет. Поэтому `prune: true` в проде обычно ходит
в паре с аннотацией `argocd.argoproj.io/sync-options: Prune=false` на объектах, потеря которых
необратима.

Одна оговорка, важная дальше: `Prune=false` закрывает объект только от **прунинга при
синхронизации** — то есть от случая «файл пропал из git». От удаления объекта **вместе с самим
`Application`** он не защищает: это замерено в демо 3, и там для этого нужна другая опция.

### Что демо после себя убрало

Всё, что оно оставило в кластере, убирается руками — потому что ArgoCD этого не сделает:
`ConfigMap manual-not-in-git` удалён, метка и аннотация `drift-probe` сняты, под `git-client`
скрипт удаляет за собой сам. Итог: репозиторий на seed-коммите
`d049270d788de26273d85992c271c5994a6d7ca8`, `Application` — `Synced/Healthy` на нём же,
`replicas: 1`, `Service` на месте.

Границы после демо не сдвинулись ни на шаг: метка `pod-security.kubernetes.io/enforce: baseline`
на namespace та же, `NetworkPolicy` `git-server-allow-stand-only` не менялась (9418/TCP из
своего namespace), `Application` в ns `argocd` по-прежнему ноль, CRD `argoproj.io` с прежним
`creationTimestamp 2026-07-19T09:08:17Z`, `ClusterRoleBinding`, ссылающихся на
`cookbook-argocd`, — ноль, боевой ArgoCD работает как работал.

## Демо 3: app-of-apps

Фикстура: `fixtures/40-app-of-apps.txt`. Манифест: `manifests/30-app-of-apps.yaml`.

Демо отвечает на два вопроса. Первый: как одним объектом описать целый набор приложений.
Второй, куда более важный на практике: **что произойдёт с этим набором, когда корневой объект
удалят**. Второй ответ снят фактом, пятью замерами, и он не совпадает с тем, что обычно
подразумевают под словом «каскад».

### Что применяется руками, а что приезжает из git

Руками применяется **ровно один** объект — `Application` `root`. Он смотрит не на каталог с
`Deployment`, а на каталог с другими `Application`:

```
manifests/30-app-of-apps.yaml   -> применяется руками (apply.sh)
  └── Application root  path: apps/children
        ├── Application child-a  (файл apps/children/child-a.yaml в git)
        │     └── Deployment payload-a  (apps/payload-a/deployment.yaml)
        └── Application child-b  (файл apps/children/child-b.yaml в git)
              └── Deployment payload-b  (apps/payload-b/deployment.yaml)
```

Для ArgoCD дочерний `Application` — такой же объект синхронизации, как `ConfigMap`: он его
просто создаёт в namespace назначения. Дальше каждый дочерний подхватывает уже свой контроллер.
Добавить приложение в набор — значит положить в git ещё один файл в `apps/children`; применять
руками ничего не нужно.

Коммит с дочерними и их нагрузкой (даты прибиты, хеш воспроизводим):
`da7ecdfe23b84d59e34ad836ef0c69ffa28e17bb`. Push сделан из пода **внутри** `cookbook-argocd`
через `git-push.sh` — `NetworkPolicy` к git-серверу не ослаблялась. Оба дочерних `Application`
и оба `Deployment` живут в `cookbook-argocd`, `destination.namespace` у всех — тот же
namespace: за его пределы у нашего ArgoCD прав нет вовсе (`Role`, не `ClusterRole`).

### Сколько ждали раскатку: 3.1 s, и пинка не было

`kubectl apply` корневого — и через **1.2 s** в кластере есть первый дочерний `Application`,
через **1.4 s** — первый `Deployment`, через **2.5 s** оба пода `Ready`, через **3.1 s** все
четыре `Application` (`root`, `child-a`, `child-b`, `demo`) — `Synced/Healthy`. Повторные
прогоны дали 2.6 s и 2.6 s. Отметки — верхние границы: шаг цикла наблюдения 0.64 s; те же
секунды видны и по `creationTimestamp`, который ставит API-сервер.

Аннотация `argocd.argoproj.io/refresh` **не применялась ни разу**, делить числа на «сколько
ждали бы сами» и «сколько с пинком» не понадобилось. И это не противоречит замеру из демо 2,
где prune ждал ~120.6 s, а фактическая длина цикла опроса гуляла 125…403 s: та задержка — про
обнаружение **новой ревизии уже синхронизированного** приложения. Здесь же и корневой, и
дочерние — **новые объекты**, контроллер получает их появление watch-событием.

Разрыв между этими двумя случаями виден в одном и том же выводе. В `18:06:41`, через 3.1 s
после `apply`, все три новых `Application` уже `Synced/Healthy` **на ревизии `da7ecdf`** — а
соседнее `demo`, синхронизированное ещё в M2, в том же выводе стоит на старом `d049270`. Тот
же коммит, тот же контроллер, тот же момент. Новую ревизию `demo` подхватил позже: в `18:11:03`
(следующая отметка с временем, блок 4) он уже на `da7ecdf`. Точный момент не засечён — это
верхняя граница «не позже 4.4 минут», но сам разрыв «новый объект — секунды, новый коммит для
старого объекта — минуты» здесь именно замер, а не рассуждение.

### Чем корневой связан с дочерними: не `ownerReferences`

Снято фактически, и это ключ ко всему остальному:

```
Application child-a  ownerReferences: <none>
Application child-b  ownerReferences: <none>
Deployment  payload-a ownerReferences: <none>

annotations:
  child-a:   argocd.argoproj.io/tracking-id: root:argoproj.io/Application:cookbook-argocd/child-a
  payload-a: argocd.argoproj.io/tracking-id: child-a:apps/Deployment:cookbook-argocd/payload-a
```

`ownerReferences` нет ни на одном уровне; метки `app.kubernetes.io/instance` тоже нет (ключ
`application.resourceTrackingMethod` в `argocd-cm` не задан — у v3.4.1 по умолчанию работает
отслеживание аннотацией). Связь «кто чей» существует **только внутри ArgoCD**. Сборщик мусора
Kubernetes про эту аннотацию не знает и каскадного удаления по ней не делает.

Само дерево при этом видно в статусах: `status.resources` корневого — два `Application`,
`status.resources` каждого дочернего — его `Deployment`.

### Финалайзеров нет — их не навешивает никто

```
Application root    finalizers: пусто
Application child-a finalizers: пусто
Application child-b finalizers: пусто
Application demo    finalizers: пусто
```

ArgoCD **не добавляет** `resources-finalizer.argocd.argoproj.io` сам: ни на применённый руками
корневой, ни на созданные им дочерние. Он попадает в объект только если его написали в
манифесте (в `manifests/30-app-of-apps.yaml` эти две строки лежат закомментированными).

### Что фактически происходит при удалении корневого

Замер первый — как есть, без финалайзера:

```
kubectl delete application root
  объект root исчез           +0.9 s
  child-a, child-b            остались
  payload-a, payload-b        остались, те же поды (имена не менялись), RESTARTS 0
  150 s наблюдения (253 итерации) — изменений ноль
```

Удалился **ровно один объект** — тот, который удалили. Дочерние осиротели: аннотация
`tracking-id` у них по-прежнему указывает на `root`, а `root` в кластере больше нет.

Что поды не перезапускались, показывают их **имена** и счётчик рестартов, а не `AGE`:
`payload-a-dfd585f6c-fdprh` и `payload-b-7954876d66-hz9fq` — те же самые объекты, `RESTARTS 0`.
`AGE` как раз растёт и не может не расти: он считается от создания пода, и между двумя
снимками (`4m25s` в `18:11:03`, `6m24s` в `18:13:02`) он прибавил ровно прошедшие две минуты.

150 s наблюдения при цикле опроса, гуляющем до 403 s, сами по себе ничего бы не доказали —
но здесь есть свидетельство сильнее. Дочерние всё это время были не «замороженными остатками»,
а `Synced/Healthy` **на ревизии `da7ecdf`**: контроллер их обслуживал. Дальше они пережили
замер self-heal на `+208 s` от удаления корневого (дрейф откачен — значит сверка шла), и убрать
их пришлось руками на `+249 s`. Отложенного каскада, который «просто не успел», за это время
не появилось ни в одном признаке.

И это не мёртвый мусор в etcd. Осиротевший дочерний `Application` продолжает работать как
полноценное приложение: ручной `scale --replicas=3` на его нагрузке был виден в API (две
итерации подряд `replicas=3`) и откачен self-heal **за 0.6 s**. То есть приложение живёт,
чинит дрейф и держит состояние из git — но им самим больше не управляет ничто: файла, из
которого оно появилось, в кластере не представлено, а git о его удалении не знает.

Числа `0.6 s` здесь и `1.6 s` в демо 2 сравнивать между собой нельзя: это не разные скорости,
а разные линейки. Оба — верхние границы клиентского цикла опроса, только в демо 2 цикл ходил
раз в ~1.2 s, а здесь — раз в ~0.2 s. Сам `apply` в демо 2 по логу контроллера занял `0.486 s`,
что с `0.6 s` сходится, а с `1.6 s` разошлось бы. Механизм один и тот же, точность измерения
разная.

Замер второй — тот же корневой, но с финалайзером:

```
kubectl delete application root   (metadata.finalizers: resources-finalizer.argocd.argoproj.io)
  child-a исчез               +1.0 s
  child-b исчез               +1.1 s
  объект root исчез           +0.9 s
  payload-a, payload-b        ОСТАЛИСЬ, живы и через 90 s
```

(Отметки корневого и дочерних здесь различаются меньше чем на шаг цикла наблюдения 0.6 s —
кто из них исчез первым, этот замер не решает; важно, что все трое ушли за ~1 s.)

Финалайзер на корневом обрывает каскад **на первом уровне**: дочерние `Application` ArgoCD
удалил (для корневого они и есть «его ресурсы»), а их нагрузку — нет, потому что у самих
дочерних финалайзера нет. Сироты не исчезли, они переехали на уровень ниже. Ровно то же
получается и при ручном удалении дочернего: `kubectl delete application child-a child-b`
оставил оба `Deployment` работать (проверено 20 s спустя — те же поды по именам, `RESTARTS 0`).

Замер третий — финалайзер на всех уровнях:

```
  child-a исчез               +1.5 s
  payload-a исчез             +1.6 s
  payload-b исчез             +1.7 s
  поды нагрузки исчезли       +1.8 s
  объект root исчез           +2.4 s   (держался финалайзером, пока чистил своё)
  child-b исчез               +2.5 s
```

Сквозной каскад, за 2.5 s, namespace чист. Порядок внутри этих двух секунд читать не стоит:
шаг цикла наблюдения 0.6 s.

| финалайзер стоит | корневой | дочерние `Application` | полезная нагрузка |
|---|---|---|---|
| нигде (по умолчанию) | удалён, 0.9 s | **остались работать** | **осталась работать** |
| только на корневом | удалён, 0.9 s | удалены, 1.0 / 1.1 s | **осталась работать** |
| на всех уровнях | удалён, 2.4 s | удалены, 1.5 / 2.5 s | удалена, 1.6 / 1.7 s |

Вывод, который стоит забрать в прод: каскад — свойство не паттерна app-of-apps, а финалайзера,
и он идёт ровно на ту глубину, на которую этот финалайзер проставлен. Фраза «удалил корневой —
удалилось приложение» по умолчанию неверна.

### Обратная сторона: чем закрыть от каскада отдельный объект

Финалайзер на всех уровнях означает, что одна команда `kubectl delete application root` сносит
весь набор приложений вместе с их данными. Что подставить под удар нельзя — понятно (`PVC`,
`Secret`, база), непонятно чем закрывать. Кандидатов два, и это **разные** опции: `Prune=false`
отменяет удаление объекта при **синхронизации** (файл пропал из git), `Delete=false` — при
**каскадном удалении самого `Application`**. Обе проверены на том же каскаде, что в замере
третьем; в каждом прогоне один объект нагрузки помечен, второй оставлен контролем:

| прогон | `payload-a` | `payload-b` | что стало |
|---|---|---|---|
| 1 | `Delete=false` | не помечен | **`payload-a` пережил каскад** (жив через 90 s), `payload-b` удалён за 1.7 s |
| 2 | не помечен | `Prune=false` | удалены **оба**: 1.7 s и 1.8 s — разница меньше шага цикла 0.6 s |

Остальное в обоих прогонах прошло как в замере третьем: дочерние `Application` и корневой ушли
за ~1…2.6 s. Единственное отличие первого прогона от замера третьего — аннотация на одном
объекте; всё прочее (git, манифест, финалайзеры) то же самое.

Итог однозначный: **от каскадного удаления закрывает `Delete=false`, а не `Prune=false`**.
Вторая опция про другой сценарий и здесь бесполезна — помеченный ею объект каскад забрал
наравне с непомеченным.

Аннотация ставилась `kubectl annotate` на живой объект (как и финалайзер — чтобы сравнение шло
на одном и том же содержимом git); `selfHeal` её не снял, дочернее приложение осталось `Synced`.
Причина та же, что у финалайзера: аннотации нет ни в git, ни в `last-applied-configuration`, и
трёхстороннее слияние это поле не трогает.

И ещё одно, что видно только на замере: `Delete=false` **не сохраняет приложение — он сохраняет
объект**. У выжившего `Deployment` аннотация `tracking-id` по-прежнему указывала на `child-a`,
которого в кластере уже не было. То есть спасённая от каскада база превращается ровно в такую же
сироту, как в замере первом, и прибирать её потом всё равно руками.

### В каком состоянии оставлен стенд

Стенд оставлен с **одним** `Application` — `demo`, как было к концу M2: корневой и дочерние
сняты полностью, нагрузка убрана, объектов демо 3 в namespace нет. Причина — в M4 в стенд
приезжают Argo Rollouts со своими CRD и контроллером, и стартовать они должны с понятной
картины; квота namespace считает поды штуками (16), лишние две единицы там не нужны.

Содержимое демо 3 остаётся в git (`apps/children`, `apps/payload-a`, `apps/payload-b`), и пока
git-сервер жив, демо поднимается одной командой:

```bash
bash apply.sh manifests/30-app-of-apps.yaml
```

Оговорка, без которой это неправда: репозиторий лежит в `emptyDir` пода git-сервера, а
seed-`ConfigMap` `git-seed` (`manifests/05-git-server.yaml`) содержит только контент M0. После
пересоздания пода `apps/children` и `apps/payload-*` исчезнут, и корневой `Application` начнёт
смотреть в несуществующий путь. Долить содержимое обратно — готовый блок в шаге 8 «Порядка
запуска»; он воспроизводит коммит `da7ecdf` байт в байт (проверено: пересборка дала тот же хеш,
`git diff` против кластера пуст).

Границы после демо на месте: метка `pod-security.kubernetes.io/enforce: baseline` не менялась,
восемь `NetworkPolicy` (включая `git-server-allow-stand-only`) на месте, `ClusterRole` и
`ClusterRoleBinding` стенда — ноль, CRD `argoproj.io` с прежним `creationTimestamp`
`2026-07-19T09:08:17Z`, `Application` в ns `argocd` — ноль, временный под `git-client` удалён.

## Демо 4: canary с анализом метрик и автооткат

Фикстуры: `fixtures/50-rollouts.txt` (установка и границы), `fixtures/60-canary.txt`
(первичная раскатка, пошаговое продвижение, успешный анализ), `fixtures/70-rollback.txt`
(★ автооткат с замером времени), `fixtures/80-bluegreen.txt` (blue-green).
Манифесты: `manifests/40-rollouts.sh`, `41-rollout-canary.yaml`, `42-analysis.yaml`,
`43-rollout-bluegreen.yaml`.

GitOps приводит кластер к состоянию из git — но «привёл» и «выкатил безопасно» это не одно и
то же. `Deployment` меняет все поды разом: плохая версия попадает под весь трафик и остаётся
там, пока кто-то не заметит и не откатит руками. Progressive delivery отвечает на этот разрыв:
раскатка идёт долями, между долями смотрят на метрики, и **решение catch-or-rollback принимает
не человек, а сам контроллер**. Демо снимает это на Argo Rollouts — и главное в нём не то, что
canary дополз до 100 %, а то, что при плохой метрике он **сам** вернулся на прежнюю версию.

### Единственная кластерная установка стенда — и почему она безопасна

M0–M3 не поставили в кластер ни одного кластерного объекта: ArgoCD стенда namespace-scoped,
CRD `argoproj.io` он переиспользует чужие. M4 — исключение: Argo Rollouts определяет **свои**
CRD, а CRD в Kubernetes объект кластерный, в namespace его не спрятать. Установка (`40-rollouts.sh`)
поэтому обставлена проверками, а не «`kubectl apply` и поехали»:

1. **Предполётная проверка отсутствия.** Перед установкой скрипт убеждается, что CRD Rollouts
   в кластере нет вовсе. Если есть — останавливается: значит, их поставил кто-то ещё, и
   перезапись схемы под чужими объектами недопустима. На съёмке было чисто:

   ```
   CRD Rollouts не найдено — можно ставить. Для контраста, чужие CRD argoproj.io на месте:
   applications.argoproj.io       2026-07-19T09:08:17Z
   applicationsets.argoproj.io    2026-07-19T09:08:17Z
   appprojects.argoproj.io        2026-07-19T09:08:17Z
   ```

2. **Ровно пять своих CRD, и это доказано, а не обещано.** Rollouts и ArgoCD живут в **одной**
   группе `argoproj.io`, но CRD различаются по имени (`<plural>.<group>`), и это ровно тот
   случай, где имя надо сверять машиной. Скрипт извлекает CRD из релизного `install.yaml`,
   сверяет каждое имя с белым списком Rollouts и с чёрным списком чужих
   (`applications`/`applicationsets`/`appprojects`/`imageupdaters`) — пересечение остановило бы
   установку:

   ```
   CRD в install.yaml: analysisruns analysistemplates clusteranalysistemplates experiments rollouts (.argoproj.io)
   ПРОВЕРКА: 5 CRD Rollouts, все в белом списке, пересечения с чужими нет
   ```

   После установки чужие CRD сохранили `creationTimestamp 2026-07-19` (их не перехватили), а
   пять наших получили сегодняшний — то есть это именно **добавление**, а не подмена.

3. **Контроллер — namespace-scoped, и ни одной кластерной роли от стенда.** Берётся
   `namespace-install.yaml` (флаг `--namespaced`, `Role`+`RoleBinding` в своём namespace). Три
   `aggregate`-`ClusterRole` из этого манифеста (они лишь подмешивают глаголы rollout во
   встроенные `admin`/`edit`/`view`) **вырезаются** — стенду они не нужны, а обещание «стенд не
   создаёт кластерных ролей», данное ещё в M1, должно оставаться числом:

   ```
   вырезано ClusterRole/ClusterRoleBinding: argo-rollouts-aggregate-to-admin …-edit …-view
   ПРОВЕРКА: ClusterRole/ClusterRoleBinding в наборе контроллера = 0
   ```

   Итог по границам после установки: `clusterrolebinding` на наш `ServiceAccount argo-rollouts`
   — **ноль**, контроллер `quay.io/argoproj/argo-rollouts:v1.9.1` с аргументом `--namespaced`,
   боевых `Application` в ns `argocd` по-прежнему ноль. Версия пинуется точно (v1.9.1 —
   последний стабильный релиз на момент съёмки); берутся **релизные** ассеты, а не сырые
   `manifests/` из тега: последние тянут образ `:latest` и не содержат CRD.

Квота стенда считает поды штуками (потолок 16, см. «Демо 1»). Контроллер добавил один под
(`limits.cpu 4→5` из 8), демо-нагрузка canary/blue-green в пике держала ещё 3–4 — под потолок
уложились без правки квоты.

### Метрики берутся из реального Prometheus фазы 1 — без ослабления чужих политик

Анализ должен смотреть на **живой** источник, иначе это не анализ. Берётся Prometheus стендов
фазы 1 (`prometheus.cookbook-k8s.svc.cluster.local:9090`). Прежде чем на него закладываться,
проверены две вещи, а не приняты на веру:

- **Доступность сети.** В `cookbook-k8s` нет ни `NetworkPolicy`, ни `CiliumNetworkPolicy`, а под
  контроллера Rollouts в `cookbook-argocd` не селектится ни одной из восьми политик стенда —
  значит, для него egress по умолчанию открыт. Проба из пода `cookbook-argocd` в
  `cookbook-k8s:9090` вернула `max(up)=1`, `rc=0`. Поэтому **своя egress-политика не
  понадобилась, а чужие политики не ослаблялись** — ни одной новой политики в `cookbook-k8s`
  стенд не создал.
- **Измеримость метрики.** `up{job="cookbook-k8s-pods"}` на стенде реально есть и равен 1
  (цель — `metricgen` фазы 1). Это и берётся запросом `max(up{job="cookbook-k8s-pods"})`.

`AnalysisTemplate prometheus-up` параметризован порогом: `successCondition: result[0] >=
{{args.threshold}}`. Один и тот же шаблон при `threshold=1` даёт успех (`1>=1`), при
`threshold=5` — заведомый провал (`1>=5` ложно). Так автооткат в шаге 5 запускается **настоящей
метрикой с настоящим числом**, а не подсунутой заглушкой.

### Canary: пошаговое продвижение и успешный анализ

`kind: Rollout` вместо `Deployment` (`41-rollout-canary.yaml`): тот же `PodSpec`, но выкатка
идёт шагами `setWeight`+`pause`, с анализом посередине. Версии различаются меткой `version` и
`env VERSION` (образ один — `pause:3.10`; анализ смотрит во внешний Prometheus, содержимое
образа неважно). Паузы — по длительности, чтобы демо шло само: локальный плагин
`kubectl argo rollouts` намеренно не ставится, состояние читается через `kubectl get rollout
-o jsonpath` и `AnalysisRun`/события.

Первичное создание `Rollout` canary-шаги **пропускает** (не с чем сравнивать) — сразу две реплики
v1, `Healthy`. Интересна вторая раскатка, v1→v2, снятая пошагово с `date -u`:

```
20:47:33.659  TRIGGER version v1->v2
20:47:35.457  step=0  pods=[v1 v1 v2]         setWeight 25 — canary-под v2 поднят
20:47:38.039  step=1  Paused                  пауза 20 s
20:47:55.982  step=3  AR=[…2-3=Running]       анализ пошёл (setWeight 50 позади)
20:48:16.280  step=5  AR=[…2-3=Successful]    анализ успешен, setWeight 75, пауза 20 s
20:48:36.777  step=7  Healthy pods=[v2 v2]    100 %, раскатка завершена
```

Триггер→100 % = **63.1 s**, из них 40 s — две заданные паузы по 20 s. Анализ снят как **успешный**
и по факту, а не по слову: три замера подряд `value=[1] phase=Successful`, условие `result[0]>=1`,
провайдер `http://prometheus.cookbook-k8s.svc.cluster.local:9090`, запрос
`max(up{job="cookbook-k8s-pods"})`. То есть canary дошёл до 100 % **потому что** живая метрика
прошла порог.

**Важная граница демо: реального разделения трафика тут нет — и это надо назвать прямо.**
У `Rollout` (`41-rollout-canary.yaml`) не задан ни `trafficRouting`, ни обслуживающий `Service`,
а поды крутят `registry.k8s.io/pause:3.10` и запросов не обслуживают вовсе — поэтому и поле
`.status.canary.weights` осталось **пустым** (`canaryWeight=` в `fixtures/60-canary.txt`).
Разделения трафика тут нет не «из-за отсутствия traffic-router'а», а потому что нет `Service`
и обслуживающего приложения: `setWeight` меняет лишь *соотношение реплик* новой и старой версий.
С обычным `Service` `kube-proxy` уже разделял бы запросы между подами примерно по их числу —
грубо, но реально (Argo Rollouts официально называет такое распределение best effort); точная
доля, не зависящая от числа реплик, требует уже traffic-router'а (ingress или mesh).
Фактический шаг судился **по числу подов v2 против v1** (это и записано в комментарии фикстуры),
а не по трафику: наблюдалось `[v1 v1 v2]` → `[v1 v2]` → `[v1 v2 v2]` → `[v2 v2]`. При `replicas: 2`
доля округляется до целого пода (на шаге `[v1 v1 v2]` один канареечный под из трёх подов — это
~33 %, а не ровные 25 % из спека). Поэтому это честнее показать числом подов, а не ровными
процентами.

Итог по тому, что демо **доказывает** и чего **не** доказывает:

- **доказывает** — механику ступенчатого продвижения ReplicaSet (`setWeight`+`pause`), ворота
  анализа метрик и **автоматический откат** по провалу анализа (замер реакции — следующий раздел);
- **не доказывает** — фактического разделения трафика по процентам. Для настоящего progressive
  *traffic* delivery понадобились бы обслуживающее приложение, `Service`, traffic-router (ingress
  или mesh) и генератор нагрузки — этого стенд не ставил.

### ★ Автооткат: 23.5 s до полного возврата, из них реакция механизма — 2 s

Ключевой замер. Порог анализа переставляется `1→5` (patch шага; метрика `up=1` его не проходит),
и запускается новая версия v2→v3. Дальше — без вмешательства:

```
20:49:39.317  TRIGGER version v2->v3           <- T0, отсчёт пошёл
20:49:40.536  step=0  pods=[v2 v2 v3]          canary v3 поднят, setWeight 25
20:49:42.516  step=1  Paused                   пауза 20 s
20:50:00.889  step=3  AR=[…3-3=Failed]         анализ отработал и УПАЛ
20:50:02.841  Degraded abort=true pods=[v2 v2] <- T1: v3 удалён, stable v2 снова 2/2
```

**Старт→полный откат = 23.52 s** (обе отметки — часы хоста, вычитание сырое). Из них 20 s — та
самая заданная пауза `step 1` **до** шага анализа; собственно реакция механизма (провал анализа
`20:50:00.889` → откат завершён `20:50:02.841`) — **1.95 s**. Иначе говоря: как только метрика не
прошла порог, откат занял около двух секунд; остальное — сознательно заданная длительность
выкатки, а не задержка отката.

Откат подтверждён с трёх сторон, не «на глаз»:

- `AnalysisRun canary-demo-86577bfbb-3-3` — `phase=Failed`,
  `message: Metric "phase1-up" assessed Failed due to failed (1) > failureLimit (0)`, замер
  `value=[1]` (реальное значение из Prometheus) против условия `result[0] >= 5`;
- условие `Rollout`: `Progressing=RolloutAborted: Rollout aborted update to revision 3:
  Step-based analysis phase error/failed…`;
- события контроллера: `AnalysisRunFailed` → `RolloutAborted` в одну и ту же секунду `20:50:00Z`.

Механизм повёл себя ровно как задумано: провал step-анализа → `abort` → stable-версия v2 снова
развёрнута целиком (обе реплики `2/2`). После аборта `Rollout` остаётся `Degraded`: canary снят,
обслуживает прежняя стабильная ReplicaSet (v2), состояние застыло на последней рабочей ревизии до
ручного `retry`/`undo`. Это не поломка, а то поведение, ради которого берут progressive delivery:
решение об откате принимает контроллер по факту анализа, а не человек постфактум. На стенде от
провала анализа до снятия canary прошло `1.95 s`; сколько плохая версия проживёт в реальной
выкатке, зависит от того, что и как часто измеряет анализ — здоровья приложения стенд не мерил. (Как оговорено выше, реального разделения трафика стенд не измеряет —
речь о ступенчатом продвижении реплик и автооткате, а не о доле трафика на плохой версии.)

### Blue-green: переключение сервиса — и один факт, который стоил проверки

Вторая стратегия (`43-rollout-bluegreen.yaml`): обе версии живут целиком и одновременно. Активная
(blue) держит весь трафик через `activeService`, новая (green) поднимается рядом на
`previewService`. Окно «обе версии целиком» снято живьём — четыре пода `[v1 v1 v2 v2]`,
`active=758f744c89` (blue), `preview=75bddbdff` (green).

Первая редакция манифеста содержала **ошибку конфигурации**: `autoPromotionEnabled:false` +
`autoPromotionSeconds:20` в расчёте на переключение через 20 s. Документация (`v1.9.1`,
`docs/features/bluegreen.md`) прямо говорит, что при `autoPromotionEnabled:false` параметр
`autoPromotionSeconds` игнорируется, и стенд показал ровно это: контроллер встал в
`BlueGreenPause` (`startTime 20:52:49`) и на каждой сверке печатал `pause incomplete`, `active`
остался на blue. Такую паузу снимает только явный `promote`; для переключения с задержкой нужен
`autoPromotionEnabled:true` вместе с `autoPromotionSeconds`. Прежняя редакция этого файла
называла это расхождением документации с реализацией — это неверно.

Поэтому в манифесте — каноничный `autoPromotionEnabled:true` (переключить, как только preview
`Healthy`). На нём переключение снято чисто:

```
20:59:15.807  TRIGGER v1->v2
20:59:17.050  active=758f744c89(blue)  preview=75bddbdff(green)  4 пода [v1 v1 v2 v2]
20:59:19.033  active=75bddbdff(green!)  Healthy                  4 пода — blue ещё жив
20:59:48.416  active=75bddbdff(green)   2 пода [v2 v2]           blue снят
```

`active` переключился blue→green за **~3.2 s** от триггера (как только green стал `Healthy`), а
старый blue держался ещё **~29 s** под `scaleDownDelay` (дефолт 30 s) — это и есть окно
мгновенного отката: обе версии в кластере живы одновременно, `activeService` можно вернуть на blue
одним переключением селектора.

### Что демо после себя убрало

Демо-объекты (`Rollout` canary, `Rollout`+сервисы blue-green, `AnalysisTemplate`, `AnalysisRun`)
удалены. **Контроллер Argo Rollouts и пять его CRD оставлены** — они снимаются `teardown.sh`
(namespace целиком уносит контроллер, кластерные CRD — явным списком имён). Границы после демо не
сдвинулись: чужие CRD `argoproj.io` с прежним `creationTimestamp 2026-07-19`, боевых `Application`
ноль, `ClusterRole`/`ClusterRoleBinding` с `rollout` в имени ноль, привязок на SA `argo-rollouts`
на кластерном уровне ноль, метка `pod-security…/enforce: baseline` не менялась, восемь
`NetworkPolicy` на месте, в `cookbook-k8s` стенд не создал ни одной политики.

## Ограничения и особенности

Здесь собрано в одном месте всё, что важно не упустить при чтении демо выше:

1. **CRD общие с боевым ArgoCD — это свойство кластера, а не рекомендация.** `applications`,
   `applicationsets`, `appprojects` `.argoproj.io` — объекты **кластерного** уровня, они одни на
   весь кластер и принадлежат Helm-релизу боевого ArgoCD (`creationTimestamp 2026-07-19T09:08:17Z`,
   метки Helm). Поэтому ArgoCD стенда установлен **без CRD и с пином версии** `v3.4.1` — ровно той,
   что у боевого: две версии одного кластерного типа невозможны, а разные версии контроллеров
   рассчитывали бы на разные схемы. Штатный `install.yaml` в общем кластере применять нельзя — в
   нём (тег `v3.4.1`, `1 890 674` байта) три `CustomResourceDefinition` плюс тотальная `ClusterRole`
   `["*"]/["*"]/["*"]` у `argocd-application-controller`, то есть админ на весь кластер и перехват
   трёх чужих CRD. `namespace-install.yaml` того же тега (`88 049` байт) не содержит **ни одного**
   CRD и ни одной кластерной роли. Обычному читателю на **своём** кластере подходит штатный
   `install.yaml` без фильтров — ограничение здесь только у нас. Разбор — «Демо 1».

2. **«Namespace-scoped» — это два независимых решения, а не одно.** Первое — RBAC: права заданы
   `Role`/`RoleBinding` в своём namespace, а не `ClusterRole` (`11-rbac.yaml`). Второе —
   область наблюдения: cluster-scope-секрет `15-cluster-scope.yaml` задаёт `namespaces:
   cookbook-argocd` и `clusterResources: "false"`, сужая кэш контроллера. Штатный
   `namespace-install.yaml` закрывает только **первое**: с ним контроллер стартует, но на построении
   кэша всех кластерных типов спотыкается о первый же чужой CRD (`clustertriggerauthentications.keda.sh
   is forbidden`) и первый `Application` встаёт в `Unknown`. Одного из двух решений недостаточно —
   «Демо 1».

3. **git-сервер внутри кластера — осознанное упрощение стенда ради герметичности, а не
   рекомендация.** Источник правды в GitOps по определению внешний; стенд делает наоборот, чтобы
   разворачиваться одной командой без аккаунта на хостинге, токена и deploy-ключа. Цена: у
   протокола `git://` нет аутентификации, история не переживает пересоздание пода (`emptyDir`),
   webhook-ов и PR-flow нет. Seed-коммит при этом **воспроизводим** — всегда
   `d049270d788de26273d85992c271c5994a6d7ca8`: единственным источником недетерминизма были метки
   времени, и они прибиты (`GIT_AUTHOR_DATE=GIT_COMMITTER_DATE=2026-07-24T12:00:00+00:00`). В проде
   источник внешний, и вывод про «доступ на запись в репозиторий = доступ в кластер» переносится
   буквально. Разбор — «Почему git-сервер внутри кластера».

4. **Задержка опроса репозитория — 125…403 s (медиана 225), а не «2 минуты».** Это фактический
   разброс для **нашего** ненастроенного экземпляра: `timeout.reconciliation` не задан → в силе
   умолчания `appResyncPeriod=2m0s` и `appResyncJitter=1m0s` (печатает сам бинарник при старте).
   Двадцать подряд идущих плановых сверок дали интервалы `125…403 s` (`fixtures/30-selfheal.txt`,
   блок «двадцать плановых опросов»). Ключевое различие: **новый ОБЪЕКТ** (корневой app-of-apps,
   дочерние) контроллер получает watch-событием за секунды, а **новую РЕВИЗИЮ** уже
   синхронизированного `Application` — только по опросу. Отсюда разрыв в замерах: раскатка
   app-of-apps ~3 s против prune ~120.6 s. В проде опрос будит webhook от хостинга — «Демо 2», «Демо 3».

5. **backoff self-heal растёт как `2 × 3^(N−1)` с потолком 300 s.** После попытки self-heal номер N
   контроллер выдерживает `2 × 3^(N−1)` секунд (но не больше 300) и только потом разрешает попытку
   N+1; перед первой попыткой паузы нет. Потолок `300 s` не взят из `--help`, а **показан в работе**:
   формула для попытки №6 требует `2 × 3⁵ = 486 s`, а контроллер напечатал `retrying in 4m59.4s`
   (`299.4 s`). Практический вывод: если один объект правят раз за разом, окно расхождения растёт
   `2 → 6 → 18 → 54 → 162 s` и упирается в пять минут — «ArgoCD откатывает за пару секунд» верно
   ровно один раз. Разбор — «Демо 2», замер 1.

6. **От каскадного удаления защищает `Delete=false`, а не `Prune=false` — это разные опции.**
   `Prune=false` отменяет удаление объекта при **синхронизации** (файл пропал из git);
   `Delete=false` — при **каскадном удалении самого `Application`**. Проверено на одном и том же
   каскаде: помеченный `Delete=false` `payload-a` пережил удаление (жив через 90 s), помеченный
   `Prune=false` `payload-b` каскад забрал за 1.8 s. И оговорка: `Delete=false` сохраняет **объект**,
   а не приложение — спасённый `Deployment` остаётся сиротой с `tracking-id` на уже удалённого
   родителя. Разбор — «Демо 3».

7. **Метрика анализа в M4 — прокси для демонстрации механизма, причинно с самим canary не связана.**
   Анализ смотрит на `max(up{job="cookbook-k8s-pods"})` из реального Prometheus фазы 1 — это живой
   сигнал с настоящим числом (порог `1` проходит, `5` заведомо валит), но метрика доступности
   постороннего пода не отражает здоровье выкатываемой версии. Так сделано, чтобы автооткат
   запускался настоящим измерением, а не заглушкой; в проде анализ целится в сигналы **нового
   релиза** (ошибки, латентность, бизнес-метрики канареечных подов). Это честная оговорка о
   переносимости — «Демо 4».

8. **blue-green: `autoPromotionSeconds` при `autoPromotionEnabled:false` игнорируется — по
   документации.** Первая редакция манифеста рассчитывала на переключение через 20 s при
   `autoPromotionEnabled:false`; контроллер ожидаемо встал в `BlueGreenPause`, `active` остался на
   blue. Это ошибка конфигурации, а не расхождение с документацией. В манифесте —
   `autoPromotionEnabled:true` (переключить, как только preview `Healthy`), на нём переключение
   снято чисто. Разбор — «Демо 4», blue-green.

9. **Часы хоста и кластера расходятся на `+1.53 s` — поправка нужна везде, где хостовая отметка
   сравнивается с логом.** Часть отметок снята часами хоста (`kubectl` с ноутбука), часть — часами
   кластера (лог контроллера, `date` в поде); кластер идёт на `1.53 s` вперёд (три замера подряд,
   RTT ~0.2 s). Где обе отметки хостовые (возврат после дрейфа: 1.6/1.4/1.4/1.6/54.4 s) — сдвиг
   сокращается и поправка не нужна. Где рядом с хостовой встаёт отметка из лога — поправку легко
   забыть и получить бессмыслицу («объект появился раньше, чем создан»): замер prune без неё был бы
   занижен (120.3 → 121.8 s). Разбор — «Демо 2», про часы.

## Порядок запуска

1. `manifests/00-namespace.yaml` — namespace (под `pod-security` `baseline`), `ResourceQuota`,
   `LimitRange`.
2. `manifests/05-git-server.yaml` — `ConfigMap` с содержимым репозитория, Deployment
   git-сервера, Service на 9418 и `NetworkPolicy`, пускающая к нему только поды своего
   namespace. Дождаться `rollout status`.
3. `manifests/10-argocd.sh` — скачивает `namespace-install.yaml` версии `v3.4.1`, вырезает
   документы `kind: CustomResourceDefinition`, подменяет реестр redis и применяет в
   `cookbook-argocd`. Идемпотентен. Дождаться готовности:
   `kubectl -n cookbook-argocd wait --for=condition=Ready pod --all --timeout=300s`.
4. `manifests/11-rbac.yaml` — две `Role` с привязками: широкая для контроллера (он применяет
   манифесты) и узкая для `argocd-server` (`delete,get,patch` — апстримный набор). Обе —
   в пределах **своего** namespace.
5. `manifests/15-cluster-scope.yaml` — секрет-описание кластера: следить только за
   `cookbook-argocd`, кластерные ресурсы не трогать. После него перезапустить контроллер:
   `kubectl -n cookbook-argocd delete pod argocd-application-controller-0`.
6. `manifests/20-application.yaml` — `Application`. В M1 применялся без `syncPolicy` (ручной
   режим), с M2 в нём же включены `automated`, `prune` и `selfHeal` — режим переключается
   правкой этого файла, а не кнопкой в UI.
7. Демо 2 (`fixtures/30-selfheal.txt`) — ручной дрейф, граница механизма и prune. Коммит в
   демо-репозиторий делается через `git-push.sh`: скрипт поднимает временный под **внутри**
   `cookbook-argocd` (иначе `NetworkPolicy` не пустит к git-серверу), клонирует репозиторий,
   выполняет переданный на stdin набор git-команд с прибитыми `GIT_AUTHOR_DATE` /
   `GIT_COMMITTER_DATE` и удаляет под за собой:

   ```bash
   bash git-push.sh <<'EOF'
   git rm apps/demo/service.yaml
   git commit -m "remove: Service demo"
   git push origin main
   EOF
   ```

8. `manifests/30-app-of-apps.yaml` — демо 3, корневой `Application` app-of-apps
   (`fixtures/40-app-of-apps.txt`). Дочерние `Application` и их нагрузка приезжают **из git**
   (`apps/children`, `apps/payload-a`, `apps/payload-b`, коммит
   `da7ecdfe23b84d59e34ad836ef0c69ffa28e17bb`) — применять руками их не нужно, раскатка
   занимает ~3 s.

   Если репозиторий пересоздавался (`emptyDir` git-сервера, seed-`ConfigMap` содержит только
   контент M0), содержимое доливается тем же `git-push.sh`, что и в шаге 7. Блок ниже —
   готовый, выполнять до `apply.sh`. Он даёт ровно коммит `da7ecdf...`: даты прибиты в
   `git-push.sh`, личность и сообщение фиксированы, содержимое — байт в байт то же (проверено,
   пересборка вернула тот же хеш).

   ```bash
   bash git-push.sh <<'EOF'
   mkdir -p apps/children apps/payload-a apps/payload-b
   cat > apps/children/child-a.yaml <<'YML'
   # Дочерний Application. Лежит В GIT, а не применяется руками: его создаст корневой
   # Application, для которого этот каталог — обычная полезная нагрузка.
   apiVersion: argoproj.io/v1alpha1
   kind: Application
   metadata:
     name: child-a
     # Namespace задан явно: Application читает тот контроллер, в чьём namespace он лежит.
     namespace: cookbook-argocd
     labels:
       stand: cookbook-gitops
       tier: child
   spec:
     project: default
     source:
       repoURL: git://git-server.cookbook-argocd.svc.cluster.local/demo.git
       path: apps/payload-a
       targetRevision: HEAD
     destination:
       server: https://kubernetes.default.svc
       namespace: cookbook-argocd
     syncPolicy:
       automated:
         prune: true
         selfHeal: true
   YML
   cat > apps/children/child-b.yaml <<'YML'
   # Дочерний Application. Лежит В GIT, а не применяется руками: его создаст корневой
   # Application, для которого этот каталог — обычная полезная нагрузка.
   apiVersion: argoproj.io/v1alpha1
   kind: Application
   metadata:
     name: child-b
     # Namespace задан явно: Application читает тот контроллер, в чьём namespace он лежит.
     namespace: cookbook-argocd
     labels:
       stand: cookbook-gitops
       tier: child
   spec:
     project: default
     source:
       repoURL: git://git-server.cookbook-argocd.svc.cluster.local/demo.git
       path: apps/payload-b
       targetRevision: HEAD
     destination:
       server: https://kubernetes.default.svc
       namespace: cookbook-argocd
     syncPolicy:
       automated:
         prune: true
         selfHeal: true
   YML
   cat > apps/payload-a/deployment.yaml <<'YML'
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: payload-a
     labels:
       app: payload-a
       stand: cookbook-gitops
       tier: payload
   spec:
     replicas: 1
     selector:
       matchLabels:
         app: payload-a
     template:
       metadata:
         labels:
           app: payload-a
           stand: cookbook-gitops
           tier: payload
       spec:
         containers:
           - name: app
             image: registry.k8s.io/pause:3.10
             resources:
               requests:
                 cpu: 10m
                 memory: 16Mi
   YML
   cat > apps/payload-b/deployment.yaml <<'YML'
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: payload-b
     labels:
       app: payload-b
       stand: cookbook-gitops
       tier: payload
   spec:
     replicas: 1
     selector:
       matchLabels:
         app: payload-b
     template:
       metadata:
         labels:
           app: payload-b
           stand: cookbook-gitops
           tier: payload
       spec:
         containers:
           - name: app
             image: registry.k8s.io/pause:3.10
             resources:
               requests:
                 cpu: 10m
                 memory: 16Mi
   YML
   git add -A
   git commit -q -m "app-of-apps: два дочерних Application и их полезная нагрузка"
   git rev-parse HEAD
   git push origin main
   EOF
   ```

   Два момента, на которых этот блок ломается, если его переписывать. Первый: тело уходит в
   YAML-манифест пода **нераскрытым heredoc'ом**, а сам манифест собирается в неэкранированном
   heredoc хоста — поэтому в теле не должно быть `$`, `` ` `` и подстановок команд, они
   раскроются на ноутбуке. Второй: вложенные `<<'YML'` работают только потому, что `git-push.sh`
   отступает **каждую** строку тела ровно на столько же, а YAML-блок этот отступ снимает, —
   ручное выравнивание закрывающих `YML` его сломает.

   Прибирать демо 3 нужно **явно**: по умолчанию `kubectl delete application root` удаляет
   только сам корневой объект, дочерние и их `Deployment` остаются работать (замеры — в
   фикстуре). Полная уборка руками:

   ```bash
   kubectl -n cookbook-argocd delete application root --ignore-not-found
   kubectl -n cookbook-argocd delete application child-a child-b --ignore-not-found
   kubectl -n cookbook-argocd delete deploy -l tier=payload --ignore-not-found
   ```

   Стенд к M4 оставлен именно в этом состоянии: один `Application` `demo`, объектов демо 3
   в namespace нет.

9. Демо 4 — progressive delivery (`fixtures/50-rollouts.txt`, `60-canary.txt`,
   `70-rollback.txt`, `80-bluegreen.txt`). Порядок:

   ```bash
   bash manifests/40-rollouts.sh                       # CRD (кластер) + контроллер namespace-scoped
   kubectl -n cookbook-argocd rollout status deploy/argo-rollouts --timeout=180s
   bash apply.sh manifests/42-analysis.yaml            # AnalysisTemplate (метрика из Prometheus фазы 1)
   bash apply.sh manifests/41-rollout-canary.yaml      # Rollout canary v1 -> сразу 100 %
   # продвижение canary v1->v2: правка метки version + env VERSION в podTemplate
   kubectl -n cookbook-argocd patch rollout canary-demo --type=json -p '[
     {"op":"replace","path":"/spec/template/metadata/labels/version","value":"v2"},
     {"op":"replace","path":"/spec/template/spec/containers/0/env/0/value","value":"v2"}]'
   # автооткат: порог анализа 1 -> 5 (заведомо провальный), затем новая версия v2->v3
   kubectl -n cookbook-argocd patch rollout canary-demo --type=json -p '[
     {"op":"replace","path":"/spec/strategy/canary/steps/3/analysis/args/0/value","value":"5"}]'
   kubectl -n cookbook-argocd patch rollout canary-demo --type=json -p '[
     {"op":"replace","path":"/spec/template/metadata/labels/version","value":"v3"},
     {"op":"replace","path":"/spec/template/spec/containers/0/env/0/value","value":"v3"}]'
   # -> AnalysisRun Failed -> Rollout aborted -> stable снова 100 % (замер в 70-rollback.txt)

   # blue-green (по бюджету):
   bash apply.sh manifests/43-rollout-bluegreen.yaml
   kubectl -n cookbook-argocd patch rollout bluegreen-demo --type=json -p '[
     {"op":"replace","path":"/spec/template/metadata/labels/version","value":"v2"},
     {"op":"replace","path":"/spec/template/spec/containers/0/env/0/value","value":"v2"}]'
   # -> active переключается blue->green, blue держится scaleDownDelay (80-bluegreen.txt)
   ```

   Прибрать демо-объекты (контроллер и CRD ОСТАВИТЬ до `teardown.sh`):

   ```bash
   kubectl -n cookbook-argocd delete rollout canary-demo bluegreen-demo --ignore-not-found
   kubectl -n cookbook-argocd delete svc bluegreen-active bluegreen-preview --ignore-not-found
   kubectl -n cookbook-argocd delete analysistemplate prometheus-up --ignore-not-found
   kubectl -n cookbook-argocd delete analysisrun --all --ignore-not-found
   ```

## Teardown

```bash
bash teardown.sh
```

Скрипт проверяет контекст (`admin@k8s-volga`) и отказывается работать в чужом кластере. Затем:

1. **Удаляет namespace `cookbook-argocd` целиком.** В нём и git-сервер, и ArgoCD стенда, и
   демо-приложение — общего с другими стендами там нет ничего, поэтому точечная уборка по
   меткам не нужна.
2. **Удаляет кластерные CRD Argo Rollouts** (`rollouts`, `analysisruns`, `analysistemplates`,
   `clusteranalysistemplates`, `experiments` — все `.argoproj.io`). Они не лежат в namespace и
   удалением namespace не убираются.

Чего `teardown.sh` не делает:

- **Не трогает namespace `argocd`** — там боевой ArgoCD.
- **Не трогает чужие CRD** `applications.argoproj.io`, `applicationsets.argoproj.io`,
  `appprojects.argoproj.io`, `imageupdaters.argocd-image-updater.argoproj.io`. Список на
  удаление задан явными именами, а не маской, и перед первым `kubectl delete` скрипт сверяет
  его со списком чужих: пересечение — остановка с ненулевым кодом.
- **Не трогает узлы, control plane и `kube-system`.**
- **Не трогает стенды фаз 1–4** (`cookbook-k8s`, `cookbook-net`).

Скрипт идемпотентен: `--ignore-not-found` везде, повторный запуск на убранном стенде проходит
без ошибок.
