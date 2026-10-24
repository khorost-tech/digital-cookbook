# Стенд: операторы и CRD (k8s-volga)

## Назначение

Показать оператор не в теории, а на настоящем: в кластере k8s-volga уже работает
production-оператор `cert-manager`. Из одного объекта `Certificate` он порождает
цепочку (`CertificateRequest` → `Secret`) и сам поддерживает желаемое состояние.
Главное доказательство — живая идемпотентность: удаляем порождённый оператором
`Secret`, и оператор возвращает его без нашего участия, только по факту
расхождения желаемого и текущего состояния (тот же control loop, что у любого
Deployment/ReplicaSet — просто на своём, не встроенном в kube-controller-manager,
контроллере).

## Требования

- **`cert-manager` в кластере.** Стенд его не ставит и не настраивает — только
  читает (H0) и добавляет один `selfSigned` `Issuer`/`Certificate` в своём
  namespace (H0). В k8s-volga `cert-manager` уже был развёрнут до этого
  стенда, отдельно от него: `fixtures/00-certmanager-present.txt` фиксирует
  версию (`v1.17.2`) и список CRD, которые он ранее добавил в API кластера,
  как факт стороннего оператора, а не как результат работы этого стенда.
- **Права на создание CRD в кластере.** Не нужны для H0 (CRD `cert-manager`
  использует уже существующие), но обязательны для H1: стенд ставит свою CRD
  `greetings.cookbook.khorost.tech` (`manifests/20-crd-greeting.yaml`).
- **Права на создание RBAC в namespace `cookbook-k8s`.** H1 создаёт
  `ServiceAccount`, `Role` и `RoleBinding` (`manifests/21-operator-rbac.yaml`)
  — без прав на создание этих объектов в namespace оператору неоткуда взять
  доступ к API, и Deployment `greeter-operator` не сможет ничего реконсилить.

## Таблица версий

| Компонент | Версия |
|---|---|
| Kubernetes | 1.36.2 |
| cert-manager (controller/cainjector/webhook) | `v1.17.2` (`quay.io/jetstack/cert-manager-*:v1.17.2`) |
| cert-manager-webhook-selectel (ACME DNS-01 для боевых сертификатов, в стенде не используется) | `v1.4.0` |
| golang (образ пода `greeter-operator`, `go run`; тот же образ — сборочный этап Dockerfile) | `golang:1.26` |

## Границы безопасности

- **Боевые объекты cert-manager не тронуты.** `Certificate` `wildcard-volga-khorost-tech`
  (ns `argocd`) и деплой `selectel-webhook-*` (ns `cert-manager`) стенд не изменял, не
  удалял и не читал содержимое их TLS-секретов. После прогона `wildcard-volga-khorost-tech`
  подтверждён живым выводом: `READY=True`, `RENEWAL=2026-09-17T08:22:36Z` — то же плановое
  обновление, что было до стенда. Все поды `cert-manager` (`cert-manager`,
  `cert-manager-cainjector`, `cert-manager-webhook`, `selectel-webhook-*`) прошли прогон с
  `RESTARTS=0`.
- Сам оператор cert-manager (Deployment, CRD, ClusterRole) в этом стенде **только читается**
  (`kubectl get`) — ничего не изменялось и не переустанавливалось.
- В стенде — свой `Issuer` типа `selfSigned` (`manifests/10-selfsigned-issuer.yaml`):
  никаких ACME-запросов, никаких обращений к внешним УЦ. `selfSigned` даёт ту же цепочку
  reconcile, что и боевой ACME-`ClusterIssuer`, но без сетевых вызовов наружу и без риска
  задеть rate-limit или боевые ClusterIssuer'ы кластера.
- Стенды фаз 1–2 (`resources`, `autoscaling`, `stateful`, `secrets`, `networking`) в общем
  namespace `cookbook-k8s` не затронуты: все объекты стенда несут лейбл
  `stand: cookbook-operators`, `teardown.sh` удаляет строго по нему.
- **Свой оператор `greeter-operator` (H1) — узкий RBAC.** `Role` даёт только
  `get/list/watch/update` на `greetings`, `get/update/patch` на `greetings/status` и
  `get/list/create/update` на `configmaps`, всё — в границах `cookbook-k8s`. Прав на
  `delete`, на другие типы ресурсов или на другие namespace у ServiceAccount
  `greeter-operator` нет.
- **Содержимое приватных ключей и сертификатов в фикстуры не попадает.** В
  `fixtures/10-certmanager.txt` — только имена ключей `Secret` (`ca.crt`, `tls.crt`,
  `tls.key`), статусы, `conditions` и метаданные; base64-значения `.data` нигде не выводятся.

## Демо

### 00 — оператор в кластере есть (только чтение)

`fixtures/00-certmanager-present.txt`: `Deployment cert-manager` с точной версией образа
(`v1.17.2`) и список CRD, которые оператор добавил в API кластера (`certificates.cert-manager.io`,
`certificaterequests.cert-manager.io`, `issuers.cert-manager.io` и др.) — прямое доказательство,
что оператор расширяет Kubernetes API собственными типами, а не просто крутит цикл снаружи.

### 10 — цепочка reconcile и живая идемпотентность

`fixtures/10-certmanager.txt`. Из одного `Certificate demo-cert` (issuerRef на свой
`Issuer cookbook-selfsigned`) оператор породил `CertificateRequest demo-cert-1`
(`APPROVED=True READY=True`) и `Secret demo-cert-tls` типа `kubernetes.io/tls`. У самого
`Certificate` — `status.conditions`: `type=Ready status=True reason=Ready message=Certificate
is up to date and has not expired`.

Владение `Secret` cert-manager (в версии `v1.17.2`, флаг
`--enable-certificate-owner-ref=false` по умолчанию) отмечает **не** через
`metadata.ownerReferences` (в фикстуре он пуст — это реальный, а не ожидаемый заранее
результат), а через аннотацию `cert-manager.io/certificate-name: demo-cert`. Прямое
следствие пустых `ownerReferences`: `Secret` переживёт удаление `Certificate` — без
ссылки на владельца у Kubernetes нет встроенного механизма каскадного удаления, и
`Secret` останется в кластере сиротой, пока его не уберут отдельно (подробнее — раздел
Teardown ниже). Чтобы
`Secret` вообще попадал под лейбл-селектор стенда (и, соответственно, под `teardown.sh`),
в `Certificate.spec` явно указан `secretTemplate.labels: {stand: cookbook-operators}` —
без него cert-manager лейблы `Certificate` на порождённый `Secret` не копирует.

Ключи внутри `Secret` (без значений): `ca.crt`, `tls.crt`, `tls.key`.

**Живая идемпотентность.** `Secret demo-cert-tls` удалён вручную
(`kubectl delete secret demo-cert-tls`), дальше — опрос раз в 4 секунды. Реальные метки из
фикстуры:

```
2026-07-24T05:40:55Z                                              # момент delete
05:40:55 Error from server (NotFound): secrets "demo-cert-tls" not found
05:40:59 demo-cert-tls   kubernetes.io/tls   3     4s              # первый снимок, где Secret уже есть
...
creationTimestamp=2026-07-24T05:40:56Z                             # реальная метка пересозданного объекта
```

По шагу опроса (4 с) восстановление укладывается в один интервал: последняя отметка
с ошибкой — `05:40:55`, первая с объектом — `05:40:59`, т. е. не позднее 4 секунд. Точная
метка `creationTimestamp=05:40:56Z` (доступна только потому, что объект хранит её сам, в
отличие от разрешения полинга) показывает, что фактическое пересоздание уложилось
примерно в 1 секунду после удаления — опрос с шагом 4 с не поймал бы такую скорость, если
бы не эта метка. Дальше `Secret` в поле `AGE` растёт монотонно (`4s → 8s → 12s → …`) —
подтверждение, что это не повторные пересоздания, а один устойчивый объект. Никакого
участия человека после `kubectl delete` не было: `Secret` вернул сам оператор,
среагировав на расхождение желаемого состояния (`Certificate` требует существования
`Secret`) с фактическим.

### 20 — свой оператор: CRD `Greeting`, watch, reconcile, resync

Если раздел 10 показывал оператор снаружи (готовый `cert-manager`), то этот показывает
механику изнутри — на CRD и контроллере, написанных для стенда. `Greeting.spec.who` →
оператор `greeter-operator` создаёт `ConfigMap greeting-<имя>` с текстом `Привет,
<who>!` и пишет тот же текст в `Greeting.status.message` (плюс
`status.observedGeneration`). Reconcile — обычный цикл «прочитать желаемое → сравнить с
фактическим → привести к желаемому», без магии фреймворка: это видно прямо в исходнике
(`cmd/greeter-operator/main.go`).

#### Архитектурное решение: чистая stdlib и `go run` из ConfigMap

Оператор написан **без controller-runtime и client-go** — только стандартная
библиотека Go (`net/http`, `crypto/tls`, `encoding/json`, `bufio` и т. д.):

- **Доступ к API** — обычный HTTPS-клиент: токен ServiceAccount и CA кластера читаются
  из `/var/run/secrets/kubernetes.io/serviceaccount/` (не из окружения), TLS — с
  `RootCAs` из CA кластера, **не** `InsecureSkipVerify`.
- **Watch** — `GET .../greetings?watch=true` и построчное чтение потока (`bufio.Scanner`
  по телу ответа): apiserver отдаёт newline-delimited JSON, `client-go` для этого не
  нужен, годится сырой HTTP.
- **Reconcile и resync** — вручную: `reconcile(name)` на каждое событие watch плюс
  полный проход по всем `Greeting` каждые 30 с (см. ниже, почему это существенно).

Причина решения та же, что и у `metricgen` в стенде `autoscaling`: оператор
запускается как `go run /src/main.go` прямо из `ConfigMap` на стоковом `golang:1.26`
(`manifests/22-operator.yaml`) — не нужен registry для образа, не нужна загрузка
модулей (`main.go` — один файл без `go.mod`), а механика reconcile видна читателю без
слоя абстракций фреймворка. **`ConfigMap` — точная копия `cmd/greeter-operator/main.go`**
(сверено побайтово при сборке этого стенда); если правите один файл — обязательно
синхронизируйте второй.

В проде такой цикл `go run` при каждом старте пода — не вариант (холодный старт на
компиляцию, риск разъезда исходника). Поэтому рядом лежит `Dockerfile` —
multi-stage-сборка (`golang:1.26` → `gcr.io/distroless/static:nonroot`,
`USER nonroot:nonroot`) как обозначение продуктового пути. На практике для оператора
такого масштаба берут Kubebuilder или Operator SDK с `controller-runtime`
(informer'ы с локальным кешем вместо ручного watch, генерация CRD и RBAC из
Go-тэгов, встроенный leader election) и собирают из него именно такой образ.

RBAC (`manifests/21-operator-rbac.yaml`) — узкий по построению: `greetings`
(`get/list/watch/update`), `greetings/status` (`get/update/patch`), `configmaps`
(`get/list/create/update`) в границах `cookbook-k8s`, и ничего больше. Материал для H2.

#### Реальные результаты

Фикстура: `fixtures/20-own-operator.txt`. Сразу после `apply` `Greeting demo`
(`spec.who: "мир"`):

```
NAME                                  WHO   MESSAGE
greeting.cookbook.khorost.tech/demo   мир   Привет, мир!
```

`status`, заполненный оператором: `{"message":"Привет, мир!","observedGeneration":1}`.
Порождённый `ConfigMap greeting-demo`: `{"message":"Привет, мир!"}`.

**Порча `ConfigMap` вручную и наблюдаемое поведение.** `kubectl patch configmap
greeting-demo` подменяет `data.message` на `испорчено вручную` в `05:56:02Z`; дальше —
опрос раз в 5 с:

```
05:56:02 испорчено вручную
05:56:07 испорчено вручную
05:56:13 Привет, мир!          # первый снимок после исправления
```

**Реакция НЕ мгновенная** — и это не баг, а прямое следствие архитектуры: оператор
подписан через watch только на `Greeting`, а не на порождённые им `ConfigMap`. Правку
чужой рукой в `ConfigMap` увидеть через watch неоткуда — событие о ней в API вообще не
приходит на тот watch, что открыт у оператора. Лог оператора (с точными метками)
подтверждает источник исправления:

```
2026-07-24T05:56:10.890948380Z resync: полный проход, найдено Greeting: 1
2026-07-24T05:56:10.893691769Z reconcile demo: ConfigMap greeting-demo расходится с
  желаемым (было "испорчено вручную", нужно "Привет, мир!") — обновляю
```

Исправление пришло на плановом `resync` (тикер каждые 30 с от старта пода — старт в
`05:55:10`, тики в `05:55:40`, `05:56:10`, `05:56:40`…), а не через watch: `05:56:10`
как раз попадает в тик резюнка. Порча в `05:56:02` — «мимо» ближайшего предыдущего тика
(`05:55:40`), поэтому оператор увидел расхождение только на следующем — `05:56:10`,
т. е. фактическая задержка исправления ~8 с, но она полностью объясняется временем
до ближайшего тика резюнка, а не задержкой обработки события.

**Смена `spec.who` — обратный случай.** `kubectl patch greeting demo --type merge -p
'{"spec":{"who":"Kubernetes"}}'` в `05:57:28Z`; лог с точными метками:

```
2026-07-24T05:57:29.661638420Z watch: событие MODIFIED greeting=demo (spec.who="Kubernetes") — запускаю reconcile
2026-07-24T05:57:29.665250436Z reconcile demo: ConfigMap greeting-demo расходится с желаемым (было "Привет, мир!", нужно "Привет, Kubernetes!") — обновляю
2026-07-24T05:57:29.671306283Z reconcile demo: status обновлён (message="Привет, Kubernetes!", observedGeneration=2)
```

Изменение `spec` попадает в watch на `Greeting`, поэтому реконсиляция срабатывает по
событию, а не ждёт резюнка — но точная оценка задержки требует аккуратности с
точностью меток. Момент `patch` зафиксирован командой `date` с точностью до секунды
(`05:57:28Z`), а момент реакции — из лога оператора с точностью до миллисекунды
(`05:57:29.661638420Z`). Секундная метка не говорит, в какой момент внутри секунды
`05:57:28` произошёл сам `patch`: если это было в начале секунды (`05:57:28.000`),
разница — около 1,66 с; если в конце (`05:57:28.999`) — около 0,66 с. Честная граница
по этим меткам — **до ~1,7 с**, а не «меньше секунды»: у секундной точности исходной
метки нет запаса, чтобы утверждать субсекундную реакцию. Направление вывода при этом
не меняется — это на порядки быстрее реакции через резюнк (раздел выше: до ~30 с,
здесь по факту ~8 с). `ConfigMap` и `status` обновились синхронно (`Привет,
Kubernetes!`, `observedGeneration=2`).

**Идемпотентность видна прямо в логе.** После записи `status` apiserver присылает ещё
одно событие `MODIFIED` (сам оператор изменил объект — получает уведомление о своей же
записи); на нём `reconcile` явно фиксирует отсутствие работы:

```
2026-07-24T05:57:29.671321456Z watch: событие MODIFIED greeting=demo (spec.who="Kubernetes") — запускаю reconcile
2026-07-24T05:57:29.674254683Z reconcile demo: ConfigMap greeting-demo уже в желаемом состоянии (message="Привет, Kubernetes!") — изменений не требуется
2026-07-24T05:57:29.674263566Z reconcile demo: status уже актуален (message="Привет, Kubernetes!", observedGeneration=2) — не трогаю
```

То же самое видно на каждом «пустом» тике `resync` (`05:55:40`, `05:56:40`, `05:57:40`
в фикстуре) — «уже в желаемом состоянии… изменений не требуется» и «status уже
актуален… не трогаю» повторяются, пока `spec` не меняется и порчи нет.

#### Вывод про watch и resync

Watch на `Greeting` даёт реакцию на изменение **того, что оператор наблюдает
напрямую** (`spec`), быстро — до ~1,7 с в этом прогоне (точнее по имеющимся меткам не
сказать, см. раздел ограничений ниже). Но watch не знает о
расхождениях в объектах, которые оператор только породил (`ConfigMap`) и за
изменениями которых отдельно не следит: такие расхождения находит только резюнк, и
задержка обнаружения — до длины интервала резюнка (здесь до 30 с, по факту — то
время, что остаётся до ближайшего тика). Это ровно то, ради чего в
`controller-runtime`/client-go операторы в проде обычно ставят watch не только на
«свой» CRD, но и на все порождённые типы (через `Owns()` в Kubebuilder) — тогда чужая
правка `ConfigMap` тоже долетает как событие, а не ждёт резюнка. В этом
демо-операторе такого watch на `ConfigMap` нет намеренно: это и есть иллюстрация
разницы, а не недоделка.

### 30 — риски эксплуатации: масштаб прав оператора и залипшее удаление на finalizer

`fixtures/30-operator-risks.txt`. Два независимых наблюдения о том, почему оператор
больно эксплуатировать: широкие права по построению и finalizer, способный намертво
застопорить удаление.

#### (а) Масштаб прав: свой оператор узок, кластерный — по необходимости широк

`kubectl auth can-i --list --as=system:serviceaccount:cookbook-k8s:greeter-operator -n
cookbook-k8s` возвращает короткий список, ровно тот, что задан в
`manifests/21-operator-rbac.yaml`: `configmaps` (`get list create update`),
`greetings.cookbook.khorost.tech` (`get list watch update`),
`greetings.cookbook.khorost.tech/status` (`get update patch`) — плюс стандартный
самослужебный набор (`selfsubjectreviews` и т. п.) и `discovery`-эндпоинты
(`/api`, `/apis`, `/healthz`…). На секреты в своём namespace — `no`. В чужом namespace
(`cookbook-net`) — тоже `no`: права выданы через `RoleBinding` в `cookbook-k8s`, и
область действия за этот namespace не выходит. Область задаёт именно привязка:
`RoleBinding` может ссылаться и на `ClusterRole`, и тогда её правила действуют
только в namespace привязки.

В списке виден пример неиспользуемого права: `update` на `greetings.cookbook.khorost.tech`
выдан, но код (`cmd/greeter-operator/main.go`) пишет только в подресурс
`greetings/status` (`updateGreetingStatus`, `PUT .../greetings/{name}/status`) — прямого
`PUT`/`PATCH` на сам `greetings` в коде нет. Право есть, применения ему в
reconcile-цикле нет — материал для вывода о принципе наименьших привилегий: RBAC стоит
сверять с тем, что реально дергает клиент, а не с тем, что «может пригодиться».

Для контраста — то же самое для кластерного `cert-manager`
(`system:serviceaccount:cert-manager:cert-manager`, только чтение через `auth can-i`,
без каких-либо изменений): список заметно шире и включает `secrets`
(`get list watch create update delete patch`), `pods`, `services`, `certificates.cert-manager.io`,
`certificaterequests.cert-manager.io`, `challenges.acme.cert-manager.io`,
`ingresses.networking.k8s.io`, `httproutes.gateway.networking.k8s.io` и approve-право на
`signers.cert-manager.io` для `clusterissuers`/`issuers`. `kubectl auth can-i get secrets
--all-namespaces --as=system:serviceaccount:cert-manager:cert-manager` → `yes`: роли
привязаны `ClusterRoleBinding`, область действия — весь кластер.

Это не дефект cert-manager: он не может выпускать TLS-сертификаты в произвольные
namespace, не имея права писать `Secret` в этих namespace. Но именно поэтому у него и
шире область поражения — ошибка или компрометация кластерного оператора масштабируется
на всё, чем он управляет (весь кластер), а не на один namespace. Свой `greeter-operator`
именно поэтому нарочно сделан узким: даже полный захват его пода не даёт ничего, кроме
`configmaps` и `greetings` в `cookbook-k8s`.

#### (б) Finalizer: механизм отложенного, а не отменённого удаления

Полный цикл на своём объекте `Greeting demo` (чужие объекты не трогались):

1. Ставим `finalizer`, который никто не собирается снимать:
   `kubectl patch greeting demo --type merge -p '{"metadata":{"finalizers":["cookbook.khorost.tech/demo-finalizer"]}}'`.
2. `kubectl delete greeting demo --wait=false` в `06:06:35Z` — kubectl тут же отвечает
   `deleted from cookbook-k8s namespace`, но это отчёт о том, что запрос принят, а не о
   том, что объект пропал.
3. Объект по-прежнему на месте: `deletionTimestamp=2026-07-24T06:06:36Z
   finalizers=["cookbook.khorost.tech/demo-finalizer"]`, `kubectl get greeting demo`
   возвращает строку `demo Kubernetes Привет, Kubernetes!` — объект жив.
   `deletionTimestamp` выставлен apiserver'ом почти сразу при `delete` (запрос —
   `06:06:35Z`, метка — `06:06:36Z`), но объект не удаляется, пока список `finalizers`
   не станет пустым — это и есть механизм. Финалайзер снят только на шаге 4, в
   `06:06:53Z`, — то есть по факту объект провисел бы в этом состоянии все 18 с между
   `delete` и ручным снятием finalizer, а без него — неопределённо долго.
4. В `06:06:53Z` снимаем finalizer вручную:
   `kubectl patch greeting demo --type merge -p '{"metadata":{"finalizers":[]}}'`.
5. Объект после этого удалён — фактический ответ apiserver:
   `Error from server (NotFound): greetings.cookbook.khorost.tech "demo" not found`.
   Удаление, отложенное на шаге 2, довыполнилось сразу же, как только finalizers
   опустели — оно было отложено, а не отменено. Фикстура не содержит отдельной метки
   времени для этой проверки, поэтому точная задержка после снятия finalizer здесь не
   указывается.

Отдельно, до пересоздания `Greeting demo` (см. ниже), зафиксировано: порождённый
`ConfigMap greeting-demo` пережил удаление `Greeting demo` — `kubectl get configmap
greeting-demo` после `NotFound` на `Greeting` всё ещё возвращает объект, и
`ownerReferences` у него пуст. Тот же паттерн, что и у `Secret`, порождённого
`cert-manager` (раздел `10` выше): без `ownerReferences` порождённые объекты не
удаляются каскадно вместе с родителем — ни при обычном удалении, ни тем более при
удалении, застрявшем на finalizer.

**Практическое следствие.** Это самая частая причина «namespace не удаляется»: внутри
висит объект с непустым `finalizers`, чей контроллер мёртв, недоступен или просто не
успевает обработать снятие. Диагностика — не гадать по логам оператора, а искать
объекты с непустым `metadata.deletionTimestamp`: такой объект уже в процессе удаления,
но чего-то ждёт. Ручное снятие finalizer (как в шаге 4 выше) — крайняя мера: оно
обходит логику, которую finalizer должен был гарантировать (например, освобождение
внешнего ресурса), поэтому применимо только тогда, когда точно известно, что эта
логика уже не выполнится и её отсутствие не оставит внешний хвост.

После прогона `Greeting demo` пересоздан из `manifests/23-greeting-sample.yaml`,
оператор снова навёл `ConfigMap greeting-demo` (`{"message":"Привет, мир!"}`) и
`status` (`{"message":"Привет, мир!","observedGeneration":1}`) — стенд в рабочем
состоянии для следующей задачи.

## Ограничения и оговорки

Здесь — всё, что важно не упустить при чтении демо выше, в одном месте:

1. **Точность «до ~1,7 с» для реакции watch, не «меньше секунды».** Момент правки
   `spec.who` зафиксирован с точностью до секунды (`date` в обвязке демо), момент
   реакции — из лога оператора с точностью до миллисекунды. Секундная метка не
   говорит, в какую часть своей секунды попал сам `patch`, поэтому честная верхняя
   граница по этим меткам — до ~1,7 с, а не сформулированная точнее «меньше
   секунды». Направление вывода это не меняет: watch на порядки быстрее резюнка
   (~1,7 с против ~8–30 с).
2. **Оператор не подписан на порождённый `ConfigMap`.** Watch открыт только на
   `Greeting` (`c.greetingsPath()+"?watch=true"` в `cmd/greeter-operator/main.go`) —
   на изменения `ConfigMap greeting-<name>` отдельного watch нет. Поэтому правка
   чужой рукой самого `ConfigMap` (раздел 20 выше) доходит до оператора только на
   плановом резюнке, а не мгновенно: это прямое следствие того, что подписки на
   порождённые объекты нет, а не задержка обработки события. Настоящие операторы
   на `controller-runtime` в такой ситуации обычно ставят watch и на порождённые
   типы (`Owns()` в Kubebuilder), чтобы такая правка тоже долетала как событие.
3. **`DELETED` намеренно не удаляет порождённый `ConfigMap`.** В
   `watchOnce` (`cmd/greeter-operator/main.go`) ветка `case "DELETED"` только
   пишет в лог и ничего не удаляет — `ConfigMap greeting-<name>` не несёт
   `ownerReferences` на удалённый `Greeting`, поэтому у Kubernetes нет
   встроенного механизма каскадного удаления, а сборка мусора вручную (аналог
   `Owns()`/finalizer на стороне оператора) в код не добавлена — это осознанно
   оставлено за рамками демо, не пропущенный кейс.
4. **В `Role` есть неиспользуемое право.** `manifests/21-operator-rbac.yaml` даёт
   `update` на сам `greetings`, но код (`cmd/greeter-operator/main.go`) пишет
   только в подресурс `greetings/status` (`updateGreetingStatus`, метод `PUT` на
   `.../greetings/{name}/status`); прямого `PUT`/`PATCH` на `greetings` в коде
   нет. Право выдано, но не применяется ни в одном вызове reconcile-цикла — живой
   пример того, как RBAC дрейфует шире фактически нужного, если не сверять его с
   тем, что реально дёргает клиент.
5. **Широта прав cert-manager — не дефект, а необходимость.** `fixtures/30-operator-risks.txt`
   показывает, что `system:serviceaccount:cert-manager:cert-manager` может читать и
   писать `secrets` во всех namespace кластера (`--all-namespaces` → `yes`). Это не
   ошибка конфигурации: кластерный оператор, который должен уметь выпускать TLS в
   произвольный namespace, физически не может обойтись без прав писать `Secret` в
   этом namespace. Раздел 30 выше проводит контраст с узким `greeter-operator`
   именно для того, чтобы показать: масштаб поражения при компрометации
   пропорционален масштабу прав, а не то, что у cert-manager «слишком много» прав
   само по себе.
6. **В стендах репозитория нет `go.mod`.** `cmd/greeter-operator/main.go` — один
   файл без модуля, компиляция и запуск проверялись через `GO111MODULE=off`
   (тот же приём, что у `metricgen` в стенде `autoscaling`); в живом Deployment это
   видно по `GOFLAGS=-mod=mod` в `manifests/22-operator.yaml` — оператор
   запускается `go run /src/main.go` без резолва модулей. Продуктовая сборка
   (`Dockerfile`) собирает тот же файл через обычный `go build`, тоже без
   `go.mod` — стандартная библиотека внешних зависимостей не требует.

## Порядок запуска

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl config current-context   # должен быть admin@k8s-volga

./apply.sh manifests/10-selfsigned-issuer.yaml
sleep 25   # дать cert-manager время на CertificateRequest -> Secret

./apply.sh manifests/20-crd-greeting.yaml
./apply.sh manifests/21-operator-rbac.yaml
./apply.sh manifests/22-operator.yaml
kubectl -n cookbook-k8s rollout status deploy/greeter-operator --timeout=300s
./apply.sh manifests/23-greeting-sample.yaml
sleep 25   # go run компилируется на каждом старте пода + первый reconcile
```

Живые выводы для фикстур снимались через `./capture.sh <name> -- <kubectl-команда>`
(пример — в `capture.sh`) и точечными `kubectl` командами, дописывающими в тот же файл
(`>> fixtures/<name>.txt`) — команды приведены дословно в комментариях самих фикстур.

## Teardown

```bash
bash teardown.sh
```

Удаляет **все** объекты стенда, в том числе те, что сами по себе не уйдут:

- `issuer`, `certificate`, `certificaterequest`, `secret` (H0) и `greeting`,
  `configmap`, `deployment`, `serviceaccount`, `role`, `rolebinding` (H1) с лейблом
  `stand: cookbook-operators` в namespace `cookbook-k8s`;
- **CRD `greetings.cookbook.khorost.tech`** — удаляется последней, после того как все
  `Greeting` в `cookbook-k8s` уже сняты (расширяет API кластера, а не namespace, поэтому
  `kubectl delete crd` идёт отдельной командой без `-n`);
- **порождённые объекты без `ownerReferences`** — `Secret demo-cert-tls` (создан
  cert-manager с флагом `--enable-certificate-owner-ref=false`, см. раздел 10) и
  `ConfigMap greeting-demo` (создан `greeter-operator`, см. раздел 20) сами по себе не
  исчезнут вслед за `Certificate`/`Greeting`: у Kubernetes нет для них ссылки на
  владельца, чтобы включить каскадное удаление. Оба, впрочем, несут лейбл
  `stand: cookbook-operators` (у `Secret` — через `secretTemplate.labels` в
  `Certificate.spec`, у `ConfigMap` — оператор проставляет его сам в reconcile), поэтому
  `--ignore-not-found`-удаление по лейблу забирает их тем же проходом, что и родителя.

Namespace `cookbook-k8s` **не удаляется** — общий для всех стендов серии. cert-manager
(его Deployment, CRD, ClusterRole) и другие операторы кластера `teardown.sh` не трогает
— они вне жизненного цикла этого стенда. Идемпотентен: повторный запуск на уже пустом
стенде ничего не ломает (`--ignore-not-found` на каждой команде, включая CRD).

**Если `Greeting` не удаляется — ищите finalizer.** Раздел 30 демонстрирует ровно эту
ситуацию нарочно: `finalizers`, который никто не снимает, переводит `delete` в
подвешенное состояние — `deletionTimestamp` выставлен, а объект жив, пока список
`finalizers` не опустеет. `teardown.sh` удаляет `greeting` с `--timeout=60s` отдельной
командой и печатает диагностику при таймауте, а не висит бесконечно (`kubectl delete` по
умолчанию без `--timeout` ждёт фактического удаления сколько угодно). Если это
произошло:

```bash
kubectl -n cookbook-k8s get greeting -l stand=cookbook-operators \
  -o jsonpath='{.items[*].metadata.finalizers}'          # какой finalizer висит

kubectl -n cookbook-k8s patch greeting <name> --type merge \
  -p '{"metadata":{"finalizers":[]}}'                      # снять вручную — крайняя мера
```

Ручное снятие `finalizer` обходит логику, ради которой он был поставлен (в проде это
обычно освобождение внешнего ресурса) — уместно только тогда, когда точно известно, что
эта логика уже не выполнится и её отсутствие не оставит внешний хвост (подробнее — раздел
30 выше).

На момент финализации стенда (H3) объекты **оставлены развёрнутыми** — `Issuer`,
`Certificate`, `CertificateRequest`, `Secret`, CRD `Greeting`, `greeter-operator` и
`Greeting demo` живы в `cookbook-k8s`, чтобы статья могла ссылаться на них как на
воспроизводимое состояние; `bash teardown.sh` снимает всё целиком, когда стенд больше не
нужен.
