# Стенд: секреты в Kubernetes (k8s-volga)

Живые демо для статьи серии «Kubernetes на практике»: чем обычный `Secret` отличается
от зашифрованных значений, и какие механизмы закрывают этот разрыв.

## Назначение

Показать живьём, что кодирование в `base64` (то, как Kubernetes хранит значения
`Secret` в `data`) — это не шифрование и не защита от того, у кого есть доступ на
чтение объекта в namespace. Дальше стенд последовательно покрывает механизмы, которые
закрывают этот разрыв: `SealedSecret` (шифротекст безопасно класть в git),
`ExternalSecret` (значение вовне, в git — только ссылка) и RBAC (кто внутри namespace
может прочитать итоговый `Secret`, независимо от способа доставки).

## Требования к кластеру

- Kubernetes 1.36+ (стенд собран и проверен на `1.36.2`).
- Namespace `cookbook-k8s` (общий для всех стендов серии, создан в стенде `resources`,
  этот стенд его не создаёт и не удаляет), с политикой PodSecurity `baseline`.
- Контроллер `sealed-secrets` (обычно в `kube-system`) и External Secrets Operator
  (обычно в собственном namespace `external-secrets`) должны быть установлены в
  кластере заранее. **Оба оператора в этом кластере уже были на момент старта задачи —
  этот стенд ни один из них не ставит, не настраивает и не обновляет**, только
  пользуется ими как уже имеющейся инфраструктурой (через их CRD и сервисы). Если в
  вашем кластере их нет — сначала поставьте операторы отдельно, это вне рамок стенда.
- Права в `cookbook-k8s` на создание/удаление namespaced-объектов: `Secret`, `Pod`,
  `Deployment`, `Service`, `SealedSecret` (CRD `bitnami.com/v1alpha1`), `SecretStore` и
  `ExternalSecret` (CRD `external-secrets.io`), `ServiceAccount`, `Role`, `RoleBinding`.
  Кластерных прав (`ClusterRole`, `ClusterSecretStore` и т.п.) стенд не требует и не
  создаёт.

## Таблица версий

| Компонент | Версия |
|---|---|
| Kubernetes | 1.36.2 |
| busybox | `1.37` |
| sealed-secrets (controller, kube-system) | `0.28.0` (`docker.io/bitnami/sealed-secrets-controller:0.28.0`) |
| kubeseal (клиент, использован для этой задачи) | `0.37.0` (из образа `alpine/k8s:1.36.2`) |
| External Secrets Operator (namespace `external-secrets`, уже установлен в кластере) | `v0.14.3` (`ghcr.io/external-secrets/external-secrets:v0.14.3`) |
| Vault стенда (dev-режим, свой, `manifests/30-vault.yaml`) | `1.21.4` (`hashicorp/vault:1.21.4`), secrets engine `kv` смонтирован в `v2` |

## Границы безопасности

- Все значения в стенде — **фиктивные**, придуманные для демонстрации. Никаких настоящих
  паролей, токенов, ключей.
- Работаем только со своими объектами (label `stand: cookbook-secrets`) в `cookbook-k8s`.
  Чужие `Secret` в кластере не читаются и их содержимое не перечисляется.
- В кластере есть боевой `ClusterSecretStore vault-backend`, ходящий во внешний Vault с
  настоящими секретами — этот стенд его **не использует и не читает**.
- Контроллер `sealed-secrets` в `kube-system` — его приватный ключ этот стенд **не трогает**.

## Демо

- **`Secret` не защищает от чтения** (`manifests/10-plain-secret.yaml`,
  `fixtures/10-plain-secret.txt`) — обычный `Secret demo-plain` со `stringData` (Kubernetes
  сам кодирует значение в `data`) и под `secret-reader`, читающий его через переменную
  окружения. Живьём видно: закодированная строка (`kubectl get secret ... -o
  jsonpath='{.data.demo-password}'`) декодируется одной командой `base64 -d` в открытый
  текст, и то же значение видно изнутри пода через `$DEMO_PASSWORD`. Урок: `Secret`
  отличается от `ConfigMap` только по соглашению (не попадает в вывод `kubectl get -o yaml`
  без явного запроса `data`, не логируется по умолчанию некоторыми инструментами), но не по
  защите значения — у кого есть доступ на чтение `Secret` в namespace, у того есть открытый
  текст.

- **Sealed Secrets: реальный цикл + привязка к namespace**
  (`manifests/20-sealedsecret.yaml`, `fixtures/20-sealedsecret.txt`) — то же фиктивное
  значение, но зашифрованное публичным ключом контроллера `sealed-secrets` (kube-system,
  версия `0.28.0`), а не хранящееся в git открытым текстом/base64 как в предыдущем демо.

  **Архитектура.** У контроллера в кластере есть пара ключей RSA: приватный ключ хранится
  внутри кластера как обычный `Secret` в `kube-system` и никогда его не покидает; публичный
  сертификат контроллер сам отдаёт всем желающим по HTTP (`GET /v1/cert.pem` на сервисе
  `sealed-secrets.kube-system.svc:8080` — без аутентификации, это не секрет). Кто угодно с
  публичным сертификатом может зашифровать значение в `SealedSecret` (утилитой `kubeseal`),
  но расшифровать обратно — может только контроллер, у которого есть приватный ключ. Поэтому
  зашифрованный `SealedSecret` **безопасно класть в git** (это и есть весь смысл механизма):
  утечка репозитория не даёт открытого текста, а `Secret` внутри кластера контроллер создаёт
  сам, применив приватный ключ, который наружу не выходит.

  **Как запускался `kubeseal`.** Локально бинарник не устанавливался и не скачивался —
  вместо этого одноразовый под с образом `alpine/k8s:1.36.2` (публичный образ-тулбокс,
  версия подобрана по версии кластера; официальный образ контроллера
  `bitnami/sealed-secrets-controller` содержит только сам контроллер, `kubeseal` в нём нет —
  проверено `kubectl run` с разными путями к бинарнику, все упали с `executable file not
  found`) поднимался в `cookbook-k8s`, внутри него `kubeseal` обращался к `/v1/cert.pem`
  контроллера и шифровал фиктивное значение. Копипаст:

  **⚠️ Расхождение версий (учесть при повторении).** Клиент `kubeseal` здесь — `0.37.0`,
  контроллер `sealed-secrets` в кластере — `0.28.0`: клиент новее контроллера на несколько
  минорных версий. Для операций этого стенда (шифрование значения, вывод в `--format yaml`)
  расхождение проблем не создало, но это не гарантия на будущее — новый клиент может
  поддерживать флаги или поведение (например, режимы `scope`), которых у более старого
  контроллера ещё нет. При повторении стоит сверять версии `kubeseal` и контроллера и, если
  есть выбор, предпочитать клиент той же (или ближайшей) версии, что и контроллер.

  **⚠️ Тег образа не пинит версию `kubeseal`.** Образ `alpine/k8s:1.36.2` каталогизирован по
  версии Kubernetes (`1.36.2`), а не по версии `kubeseal` внутри него — тот же тег при
  пересборке образа выше по течению в будущем может подтянуть другую версию `kubeseal`.
  Поэтому перед использованием стоит проверять фактическую версию явно: `kubeseal --version`
  внутри пода, — а не полагаться на тег образа.

  ```bash
  export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"

  kubectl -n cookbook-k8s run kubeseal-runner --image=alpine/k8s:1.36.2 \
    --restart=Never --command -- sleep 600

  # проверить фактическую версию клиента перед использованием (см. предупреждение выше) —
  # тег образа сам по себе версию kubeseal не гарантирует
  kubectl -n cookbook-k8s exec kubeseal-runner -- kubeseal --version

  kubectl -n cookbook-k8s exec kubeseal-runner -- sh -c \
    'curl -sS -o /tmp/cert.pem http://sealed-secrets.kube-system.svc:8080/v1/cert.pem'

  kubectl -n cookbook-k8s exec kubeseal-runner -- sh -c 'cat <<EOF > /tmp/secret.yaml
  apiVersion: v1
  kind: Secret
  metadata:
    name: demo-sealed
    namespace: cookbook-k8s
    labels: { stand: cookbook-secrets }
  type: Opaque
  stringData:
    demo-password: cookbook-sealed-not-a-real-secret
  EOF
  kubeseal --cert /tmp/cert.pem --format yaml < /tmp/secret.yaml' > kubernetes/secrets/manifests/20-sealedsecret.yaml

  kubectl -n cookbook-k8s delete pod kubeseal-runner
  ```

  Публичный сертификат и плейнтекст-исходник существовали только внутри одноразового пода
  (и, для просмотра, во временном файле вне репозитория) и удалены вместе с ним; в
  репозиторий попал только зашифрованный результат.

  **Результат разворачивания.** После `kubectl apply -f manifests/20-sealedsecret.yaml`
  контроллер сам создал `Secret demo-sealed` в `cookbook-k8s` с исходным фиктивным значением
  (`cookbook-sealed-not-a-real-secret`) и `ownerReferences`, указывающим на `SealedSecret`
  как на владельца — удаление `SealedSecret` удаляет и `Secret`.

  **Привязка к namespace и имени.** Тот же зашифрованный blob, применённый в другом
  namespace (`cookbook-k8s-alt`, sed-подмена `namespace:` в манифесте), **не разворачивается**:
  `Secret` не появляется вообще, а у `SealedSecret` — `status.conditions[].type: Synced,
  status: "False"` с точным текстом ошибки `no key could decrypt secret (demo-password)`, тот
  же текст — в логах контроллера (`Event(...): type: 'Warning' reason: 'ErrUnsealFailed'
  Failed to unseal: no key could decrypt secret (demo-password)`). Это поведение по умолчанию
  (scope `strict`): шифротекст криптографически привязан к паре `namespace/name`, для которой
  шифровался, — скопировать `SealedSecret` в другой namespace или переименовать бесполезно,
  расшифровка просто не пройдёт. Полный вывод — `fixtures/20-sealedsecret.txt`.

  **Что НЕ проверялось в этом стенде.** Ротация приватного ключа контроллера и восстановление
  после его потери — отдельная эксплуатационная тема со своими процедурами (несколько активных
  ключей, ре-шифрование существующих `SealedSecret`). Здесь боевой ключ контроллера не
  трогался вообще — ни экспорта, ни ротации, ни удаления не выполнялось, использовался только
  публичный сертификат.

- **ExternalSecret: секрет живёт вовне, в кластер только синхронизируется**
  (`manifests/30-vault.yaml`, `manifests/31-secretstore.yaml`, `manifests/32-externalsecret.yaml`,
  `fixtures/30-externalsecret.txt`) — третий, принципиально другой подход к тому же вопросу.
  `Secret` и `SealedSecret` выше хранят значение (открытым или зашифрованным) внутри
  кластера/git. `ExternalSecret` — наоборот: исходное значение живёт **во внешнем
  хранилище** (Vault), в git и в кластер попадает только *ссылка* на путь и ключ, а
  оператор (External Secrets Operator, ESO) периодически синхронизирует актуальное
  значение во внутренний `Secret`.

  **Почему стенд поднимает собственный Vault, а не использует боевой `ClusterSecretStore
  vault-backend`.** В кластере уже есть `vault-backend` — он ходит во внешний Vault
  автора с настоящими секретами. Использовать его здесь означало бы две проблемы
  сразу: (1) стенд трогал бы боевые данные ради демо на фиктивных значениях — риск без
  необходимости; (2) стенд перестал бы быть воспроизводимым — у читателя, повторяющего
  пример на своём кластере, боевого Vault автора попросту нет и быть не может. Поэтому
  `manifests/30-vault.yaml` разворачивает **отдельный Vault в dev-режиме** внутри
  `cookbook-k8s` (хранилище — в памяти, без персистентности, это нормально для
  одноразового демо), а `manifests/31-secretstore.yaml` создаёт **namespaced** `SecretStore
  vault-stand` (не `ClusterSecretStore`!), указывающий только на этот свой Vault
  (`http://vault.cookbook-k8s.svc:8200`). Боевой `vault-backend` в этой задаче не
  читался, не упоминался в манифестах и его конфигурация не копировалась ни в каком
  виде — обе причины границы см. также в разделе «Границы безопасности» выше.

  **PodSecurity baseline и dev-режим Vault.** Обычно dev Vault запускают с добавленной
  капабилити `IPC_LOCK` (чтобы `mlock()` не пускал секреты в своп), но `capabilities.add`
  вне разрешённого baseline-набора отклоняется admission-контроллером namespace
  (`pod-security.kubernetes.io/enforce: baseline`), а повышать привилегии namespace ради
  стенда — не вариант. Решение: `SKIP_SETCAP=true` и `VAULT_DISABLE_MLOCK=true` в
  окружении контейнера — entrypoint образа не пытается выставить `cap_ipc_lock` на
  бинарник (без `CAP_SETFCAP` это всё равно упало бы) и Vault не требует mlock. Капабилити
  контейнера при этом не трогаются вовсе (ни добавления, ни `drop: ["ALL"]` — последнее,
  наоборот, ломает встроенный в образ `su-exec`, которым entrypoint понижает привилегии
  с root до пользователя `vault`: без штатных `setuid`/`setgid` из дефолтного набора
  падает с `setgroups: Operation not permitted`).

  **Как разворачивалось.**
  ```bash
  export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
  cd G:/7/Projects/Khorost/digital-cookbook-staging

  bash kubernetes/secrets/apply.sh manifests/30-vault.yaml
  kubectl -n cookbook-k8s rollout status deploy/vault --timeout=300s

  # фиктивный секрет в свой Vault (KV подтверждён v2 по факту — путь secret/data/demo)
  kubectl -n cookbook-k8s exec deploy/vault -- sh -c \
    'VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=cookbook-root-token vault kv put secret/demo password=first-value'

  bash kubernetes/secrets/apply.sh manifests/31-secretstore.yaml
  bash kubernetes/secrets/apply.sh manifests/32-externalsecret.yaml
  ```

  **Результат синхронизации.** `SecretStore vault-stand` — `Valid`/`Ready: True`,
  `ExternalSecret demo-external` — `SecretSynced`/`Ready: True`, целевой `Secret
  demo-external` получил значение `first-value` — то же, что лежит в Vault, но
  зашедшее в кластер только через синхронизацию, не через git. Полный вывод —
  `fixtures/30-externalsecret.txt`.

  **Ротация — реальное измеренное время.** Значение в Vault сменили на `second-value` в
  `16:38:11Z` (метка перед `vault kv put`). Дальше опрашивали целевой `Secret` каждые
  5 секунд по факту UTC-времени: `16:38:12` — ещё `first-value`, `16:38:17` — ещё
  `first-value`, `16:38:22` — ещё `first-value`, **`16:38:27` — уже `second-value`**.
  То есть новое значение обнаружено в `Secret` примерно через **16 секунд** после записи
  в Vault. Это измеренное значение с учётом дискретности собственного опроса раз в
  5 секунд — сама смена произошла где-то внутри 5-секундного окна между `16:38:22`
  (замер ещё застал `first-value`) и `16:38:27` (замер уже застал `second-value`), точнее
  по этим данным не определить. `refreshInterval: 15s` в `ExternalSecret` задаёт
  периодичность, с которой ESO сам обращается к Vault за актуальным значением, — это не
  гарантия верхней границы задержки синхронизации: если изменение в Vault происходит сразу
  после очередного опроса ESO, до следующего пройдёт почти весь интервал, поэтому
  измеренные ~16 секунд при заявленных 15 секундах — ожидаемая величина, а не аномалия.
  Все метки — реальные, из `fixtures/30-externalsecret.txt`, а не подписаны вручную.

  **Потребители не перечитывают секрет автоматически.** Под `external-secret-reader`
  получает `password` из `Secret demo-external` переменной окружения `DEMO_PASSWORD` —
  а переменные окружения контейнера задаются один раз при старте процесса. После
  ротации: `Secret demo-external` уже содержит `second-value` (проверено
  `kubectl get secret ... -o jsonpath`), а `$DEMO_PASSWORD` внутри работающего,
  ни разу не перезапущенного пода (`RESTARTS: 0`) по-прежнему возвращает `first-value`.
  Это не баг ESO — оператор синхронизирует именно объект `Secret`, а не память уже
  запущенных процессов, которые этот `Secret` когда-то прочитали. Чтобы нагрузка
  увидела новое значение, её нужно перезапустить — на практике это отдельная задача,
  которая часто решается автоматической перекаткой пода по хешу содержимого `Secret`
  (например, аннотация с чек-суммой в шаблоне, которую меняет CI/CD или контроллер
  вроде Reloader), а не встроена в сам механизм ExternalSecret.

- **RBAC на чтение секретов: способ доставки не защищает от того, у кого есть право `get secrets`**
  (`manifests/40-rbac.yaml`, `fixtures/40-rbac.txt`) — три предыдущих демо закрывали вопрос
  «где хранить и как доставить значение» (открытым текстом, зашифрованным в git, синхронизацией
  из внешнего хранилища). Этот вопрос другой: «кто может прочитать итоговый `Secret` внутри
  namespace» — и на него ни `SealedSecret`, ни `ExternalSecret` не отвечают, потому что оба в
  конце концов материализуют обычный `Secret` в `cookbook-k8s`, а его чтение регулирует только
  RBAC.

  **Два `ServiceAccount` для контраста.** `limited` привязан к `Role pods-reader` — `get`,
  `list`, `watch` только на `pods`, никаких прав на `secrets` (намеренно узкая роль, комментарий
  в манифесте). `broad` привязан к `Role secrets-reader` — `get`, `list` на `secrets`. Оба —
  обычные namespaced `Role`/`RoleBinding`, ничего кластерного.

  **`kubectl auth can-i` от имени `limited`:** `get secrets` → `no`, `get pods` → `yes` — роль
  работает как задумано, доступ к подам есть, к секретам нет.

  **Для сравнения — дефолтный `ServiceAccount` namespace** (`system:serviceaccount:cookbook-k8s:default`,
  без явных байндингов от этого стенда): `auth can-i get secrets` → **`no`**. В `cookbook-k8s`
  нет наследуемых с других стендов RBAC-байндингов, дающих дефолтному SA доступ к секретам —
  зафиксировано как факт, не предполагалось заранее.

  **Реальная попытка чтения от имени `limited`** (не только `can-i`, а живой `kubectl get`):

  ```
  Error from server (Forbidden): secrets "demo-plain" is forbidden: User
  "system:serviceaccount:cookbook-k8s:limited" cannot get resource "secrets" in API group ""
  in the namespace "cookbook-k8s"
  ```

  Точный текст — из `fixtures/40-rbac.txt`. Тот же SA `kubectl -n cookbook-k8s get pods` при
  этом отдаёт нормальный список подов стенда — запрет специфичен для ресурса `secrets`, а не
  общий отказ по SA.

  **Обратная сторона: `broad` читает все три секрета стенда независимо от способа доставки.**
  `kubectl -n cookbook-k8s get secrets --as=...:broad -l stand=cookbook-secrets` возвращает
  `demo-plain` (обычный `Secret` из первого демо), `demo-sealed` (развёрнутый контроллером
  `sealed-secrets` из `SealedSecret`) и `demo-external` (материализованный ESO из `ExternalSecret`)
  — все три в одном списке, без различий по происхождению. Это и есть ключевой урок задачи:
  `SealedSecret` защищает git от утечки шифротекста, `ExternalSecret` не хранит значение в git
  вовсе — но обе схемы заканчиваются одним и тем же объектом `Secret` в кластере, а кто может
  его прочитать, решает исключительно RBAC (`Role`/`ClusterRole` с правом `get`/`list` на
  `secrets`, привязанные `RoleBinding`/`ClusterRoleBinding`). Ни шифрование блоба в git, ни
  вынос значения во внешнее хранилище это не меняют.

  **Принцип наименьших привилегий и на что смотреть в реальных ролях.** `pods-reader` в этом
  стенде — пример правильного объёма прав: только то, что реально нужно потребителю. В боевых
  кластерах источник инцидентов обычно не отдельная роль вроде `secrets-reader`, а более
  широкие конструкции, которые *попутно* дают доступ к секретам: `ClusterRole` со
  wildcard-правами (`resources: ["*"]` или `verbs: ["*"]`), встроенные `admin`/`edit`
  `ClusterRole` (у `edit` есть `get`/`list`/`watch` на `secrets` по умолчанию — это часто
  забывают, выдавая роль «для CI» или «для разработчика»), `RoleBinding`, ссылающийся на такую
  `ClusterRole` в чувствительном namespace, и сервис-аккаунты подов, которым не нужен вообще
  никакой RoleBinding, но он есть по инерции от шаблона. Проверять стоит не только явные
  `resources: ["secrets"]`, но и wildcard, и биндинги на встроенные роли.

## Что выбрать

Сравнение по фактам этого стенда, не в общем:

| | Sealed Secrets | External Secrets |
|---|---|---|
| Что попадает в git | Шифротекст (`encryptedData` в `SealedSecret`) | Только ссылка (`remoteRef.key`/`property`) на путь во внешнем хранилище — значения в git нет вовсе |
| Где живёт исходное значение | Расшифровывается внутри кластера контроллером `sealed-secrets` в обычный `Secret` | Во внешнем хранилище (в стенде — свой Vault); в кластер синхронизируется копия |
| Привязка шифротекста | К паре namespace/name (scope `strict` по умолчанию) — см. демо выше: тот же blob в другом namespace не расшифровывается | Отсутствует: значение просто читается по ключу из `SecretStore`, ничего криптографически не привязано к namespace/имени `ExternalSecret` |
| От чего зависит расшифровка/доступность | От сохранности приватного ключа контроллера `sealed-secrets` в кластере — его потеря делает существующие `SealedSecret` нерасшифровываемыми | От доступности внешнего хранилища (Vault) в момент синхронизации — если оно недоступно, `ExternalSecret` не обновится (но уже материализованный `Secret` остаётся) |
| Ротация значения | Нужно заново зашифровать и закоммитить новый `SealedSecret` | Централизованная: значение меняется один раз в хранилище, ESO подтягивает его сам (в стенде — измеренные ~16 с при `refreshInterval: 15s`, см. демо выше) |

**Важно: ни один из двух механизмов не ограничивает, кто внутри `cookbook-k8s` может
прочитать итоговый `Secret`.** Оба в конце концов материализуют обычный `Secret` —
`demo-sealed` и `demo-external` в этом стенде читаются одним и тем же `broad`
(`Role secrets-reader`) наравне с обычным `demo-plain`, без всякой разницы по
происхождению (см. демо RBAC выше). Это отдельный вопрос, который решает только RBAC:
`SealedSecret`/`ExternalSecret` закрывают «что доверить git и как доставить значение
в кластер», а не «кто внутри namespace имеет право на `get`/`list secrets`».

## Порядок запуска

```bash
export KUBECONFIG="G:/7/Projects/Khorost/architecture/terraform/k8s-volga/kubeconfig"
kubectl config current-context   # должен быть admin@k8s-volga

./apply.sh manifests/10-plain-secret.yaml
kubectl -n cookbook-k8s wait --for=condition=Ready pod/secret-reader --timeout=120s

./apply.sh manifests/20-sealedsecret.yaml
kubectl -n cookbook-k8s wait --for=condition=Synced --timeout=60s sealedsecret/demo-sealed

./apply.sh manifests/30-vault.yaml
kubectl -n cookbook-k8s rollout status deploy/vault --timeout=300s
# положить фиктивный секрет в свой Vault — команда в разделе «Демо» выше

./apply.sh manifests/31-secretstore.yaml
./apply.sh manifests/32-externalsecret.yaml
kubectl -n cookbook-k8s wait --for=condition=Ready --timeout=60s externalsecret/demo-external

./apply.sh manifests/40-rbac.yaml
```

Манифесты пронумерованы по порядку применения. Живые выводы для фикстур снимались через
`./capture.sh <name> -- <kubectl-команда>`, команды приведены дословно в комментариях самих
фикстур (`fixtures/*.txt`, строка `# cmd:`).

`Secret demo-plain`, под `secret-reader`, `SealedSecret demo-sealed` (вместе с развёрнутым
из него `Secret`), свой `Vault` стенда, `SecretStore vault-stand`, `ExternalSecret
demo-external`, под `external-secret-reader` и объекты RBAC (`ServiceAccount limited`/`broad`,
`Role pods-reader`/`secrets-reader`, соответствующие `RoleBinding`) оставлены работающими —
читатель может повторить любую команду из раздела «Демо» вживую, не разворачивая стенд
заново. Для очистки — раздел «Teardown» ниже.

## Teardown

```bash
bash teardown.sh
```

Удаляет по label `stand=cookbook-secrets` в namespace `cookbook-k8s`: все `Secret`
(`demo-plain`, `demo-sealed` — вместе со своим `SealedSecret`, `demo-external`,
`vault-stand-token`), `SealedSecret demo-sealed`, `SecretStore vault-stand`,
`ExternalSecret demo-external`, `Deployment`/`Service vault`, поды `secret-reader` и
`external-secret-reader`, объекты RBAC (`ServiceAccount limited`/`broad`,
`Role pods-reader`/`secrets-reader`, соответствующие `RoleBinding`).

**Не трогает:** namespace `cookbook-k8s` (общий для всех стендов серии, не удаляется),
операторы ESO и `sealed-secrets-controller` (их CRD, приватный ключ контроллера и сами
контроллеры вне жизненного цикла этого стенда), любые чужие объекты без label
`stand=cookbook-secrets`.

Идемпотентен: `kubectl delete ... --ignore-not-found`, повторный запуск на уже пустом
стенде завершается без ошибки.
