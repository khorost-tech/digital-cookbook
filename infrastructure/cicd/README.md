# Стенд «CI/CD на практике»

Стенд к серии `cicd-practice` на khorost.tech (3 статьи). Один стенд, три раздела —
по одному на статью. Все числа для статей берутся **только** из [`FIXTURES.md`](FIXTURES.md):
что не получено живым прогоном на этой машине, в статью не попадает.

Каждое демо самопроверяемо: оно печатает не только рабочий, но и **падающий**
вариант и, если ожидаемого контраста нет, завершается с ошибкой (`exit 1`), а не
молча даёт нулевой результат.

## Разделы

### 1. `github-actions/` — дизайн CI-пайплайна

Статья: `infrastructure/ci-pipeline-github-actions`.

- Валидируемый пример workflow: reusable workflow + composite action + matrix +
  схема OIDC-публикации.
- `scripts/lint-workflows.sh` — валидация workflow через `actionlint`.
- `scripts/cache-effect-demo.sh` — эффект кэша зависимостей: пересборка Go с
  холодным `GOCACHE`/`GOMODCACHE` против тёплого.
- `scripts/layer-cache-demo.sh` — эффект Docker layer cache и цена порядка слоёв.

> **Что здесь проверено, а что нет.** Проверено живьём: валидность workflow
> (`actionlint`), оба замера кэша, сборка и тесты приложения. НЕ проверено
> прогоном: сам конвейер в облаке GitHub Actions, обмен OIDC-токена на облачный
> доступ (нужна настроенная сторона провайдера — в шаге публикации стоит
> заглушка) и вызов reusable workflow из другого репозитория.
>
> Про внешний вызов отдельно: GitHub принимает вызываемые workflow только из
> каталога `.github/workflows` **корня** репозитория, а этот стенд лежит в
> подкаталоге — значит его `reusable-build.yml` в принципе не может быть вызван
> извне как есть. Для настоящего переиспользования workflow нужно положить в
> корневой `.github/workflows` отдельного репозитория и вызывать формой
> `{owner}/{repo}/.github/workflows/имя.yml@{commit-sha}`; плюс развязать
> composite action — относительный путь `./.github/actions/...` резолвится у
> вызывающего, а не там, где лежит workflow. Здесь показана внутренняя схема
> конвейера, а не готовый межрепозиторный пример.
>
> Облачных таймингов job'ов здесь нет по построению — их честно не воспроизвести
> локально. Локальные секунды переносятся на CI только как **характер**
> зависимости (тёплый кэш заметно дешевле холодного), не как абсолютные числа.

### 2. `zero-downtime/` — деплой без простоя и миграции схемы

Статья: `architecture/zero-downtime-deploy-migrations`. Два уровня.

**Уровень A — локально (`compose/` + `loadgen/`), запускать из WSL:**

- `scripts/naive-rename-demo.sh` — `ALTER TABLE RENAME COLUMN` под нагрузкой роняет
  запросы старого кода (падающий вариант).
- `scripts/expand-contract-demo.sh` — то же переименование через 6 обратно-совместимых
  шагов → 0 ошибок И схема, эквивалентная наивному RENAME (проверяется скриптом).
- `scripts/trigger-timing-demo.sh` — с какого момента действует триггер, созданный
  под конкурентной нагрузкой: 8 открытых соединений, барьер после COMMIT.
- `scripts/trigger-update-demo.sh` — триггер двойной записи при UPDATE: наивное
  «заполнить пустую» теряет обновление старого кода, `sql/sync-trigger.sql`
  синхронизирует обе стороны и отвергает конфликт.
- `scripts/blocking-ddl-demo.sh` — `CREATE INDEX` (полный лок) против
  `CREATE INDEX CONCURRENTLY`.

**Уровень B — живой кластер k8s-volga (`k8s/`):** окно сосуществования версий во
время rolling update — то, что локально не показать.

- `k8s/naive-rollout-demo.sh` — миграция + rollout v2 роняет ещё живые v1-поды.
- `k8s/expand-contract-rollout-demo.sh` — аддитивная миграция → выкат без ошибок.

> ⚠️ Голый `kubectl` бьёт в **прод** `k8s-tula`. Скрипты уровня B требуют явный
> kubeconfig через переменную `KUBECONFIG_VOLGA` и проверяют кластер по именам
> узлов k8s-volga перед любым действием. Namespace убирается за собой. Кластер
> периодически пересоздаётся — артефакты воспроизводимы из манифестов, версии
> снимать заново.

Запуск уровня B (образы v1/v2 должны быть в доступном кластеру registry):

```bash
export KUBECONFIG_VOLGA=/путь/к/kubeconfig              # НЕ прод k8s-tula
export IMAGE_V1=<registry>/<проект>/cicd-zdt-app:v1    # SCHEMA=old
export IMAGE_V2=<registry>/<проект>/cicd-zdt-app:v2    # SCHEMA=new
# приватный проект — pull-secret через робота:
export HARBOR_SERVER=<registry> HARBOR_USER=<robot> HARBOR_PASS=<secret>
cd zero-downtime/k8s

bash naive-rollout-demo.sh              # старые поды регрессируют (> 0)

# expand-contract требует ещё и промежуточный образ:
export IMAGE_DUAL=<registry>/<проект>/cicd-zdt-app:dual  # SCHEMA=dual
bash expand-contract-rollout-demo.sh    # регрессий нет ни в одном из двух выкатов
```

Образы собираются из `zero-downtime/k8s/app/Dockerfile` с
`--build-arg SCHEMA=old|dual|new` — **три** тега из одного файла:

- `old` — пишет и читает старую колонку;
- `dual` — пишет ОБЕ колонки, читает переходным `COALESCE(новая, старая)`.
  Именно эта версия делает откат безопасным и не даёт readiness принять `NULL`
  за корректный ответ, пока backfill ещё не прошёл;
- `new` — пишет и читает только новую колонку.

Readiness во всех трёх падает, если читаемое бизнес-значение пусто: проверять
одну лишь доступность колонки мало — `NULL` означает, что данные этой версии не
видны, и считать это успехом значило бы маскировать поломку.

Опознание кластера — по паттерну `VOLGA_PATTERN` (по умолчанию `volga`).

### 3. `release-dora/` — release engineering и DORA

Статья: `architecture/release-engineering-dora`.

- `changelog/changelog-demo.sh` — conventional commits → следующая semver-версия и
  `CHANGELOG.md` (через `git-cliff` на репозитории-фикстуре).
- `dora/dora-demo.sh` — калькулятор **пяти** метрик DORA в актуальной модели
  (2024): throughput — deployment frequency, change lead time, failed deployment
  recovery time; instability — change fail rate, deployment rework rate. Прежние
  «четыре ключа» с MTTR устарели: MTTR переопределена в 2023 (теперь считает
  только отказы, вызванные изменением), rework rate добавлена в 2024.
- KPI-театр показан на двух вариантах разметки одной и той же «театральной»
  фикстуры: видно, что устойчивость deployment rework rate к дроблению деплоев
  зависит от того, как классифицированы копии, а не следует из определения
  метрики. Устойчивы к этой накрутке в обоих вариантах только change lead time и
  failed deployment recovery time.

## Замечания по окружению

- Go-команды — с `GOPROXY=https://go.khorost.tech,direct`.
- PG-порты Docker видны из Windows-хоста нестабильно — раздел 2A гонять **из WSL**.
- Абсолютные времена host-зависимы; переносится характер зависимости, не числа.
