# SCA-снимок: воспроизводимый пример к статье

Приложение к статье [«Сканирование уязвимых зависимостей (SCA): Dependabot, osv-scanner, govulncheck»](https://khorost.tech/security/dependency-scanning-sca-dependabot-osv/).

Снимок состояния публичного репозитория `digital-cookbook` **после** ремедиации: показывает контраст «Dependabot видит 0 — osv-scanner видит остаток» и два «краевых» кейса статьи (ложное срабатывание на уровне модуля и расхождение диапазонов между базами).

## Два коммита — важно для воспроизводимости

- **Сканируемый исходник:** дерево репозитория на commit **`76ca0516c94865d6495c15910680bd6ebe910436`** — именно это состояние исходников сканировалось. (Файлы снимка в нём ещё **не** появились.)
- **Файлы снимка** (`README.md`, `osv-results.json`, `govulncheck-nats.txt`): добавлены **позже**. Поэтому не делайте `git checkout 76ca051` в основном рабочем дереве — снимок из него исчезнет. Читайте эти файлы из `main`/актуального коммита, а исходник для повторного скана берите в отдельном **worktree**.

## Инструменты (точные версии)

- `osv-scanner` **2.4.0** (именно эта версия, не `@latest`)
- `govulncheck` **v1.6.0**
- Go **1.26.3** (osv-scanner 2.4.0 требует Go ≥ 1.26.4; при `GOTOOLCHAIN=auto` команда `go` сама подтянет более новый toolchain)
- Dependabot (GitHub Advisory DB)
- Дата снимка: **2026-07-13**

## Как воспроизвести

```bash
git clone https://github.com/khorost-tech/digital-cookbook
cd digital-cookbook

# сканируемый исходник — в ОТДЕЛЬНОМ worktree на 76ca051 (файлы снимка при этом остаются в main)
git worktree add /tmp/dc-src 76ca0516c94865d6495c15910680bd6ebe910436

# ПИН версии сканера — иначе не воспроизводимо
go install github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.4.0
# --format json ОБЯЗАТЕЛЕН: по умолчанию osv-scanner печатает таблицу, а не JSON
osv-scanner scan --recursive --format json /tmp/dc-src > osv-repro.json
# сравнивать НЕ побайтово (пути G:/... vs /tmp/dc-src/... и часть полей advisory отличаются),
# а СЕМАНТИЧЕСКИ — по набору (package, version, vuln-ID); плюс помнить, что OSV — живая база

# Dependabot-счётчик (нужен gh auth) — ожидаемо 0
gh api -X GET repos/khorost-tech/digital-cookbook/dependabot/alerts \
  -f state=open --paginate | jq 'length'          # -> 0
```

> ⚠️ **OSV — живая база.** При повторном прогоне позже числа могут отличаться: advisory добавляют, уточняют диапазоны и иногда отзывают. Зафиксированный результат — в [`osv-results.json`](osv-results.json); это снимок во времени, а не инвариант.

## Результат снимка

| Источник | Находки |
|---|---|
| **Dependabot** (GitHub Advisory DB) | **0** открытых алертов |
| **osv-scanner 2.4.0** (OSV) | **2 уникальных package/version, найденных в 3 манифестах** (см. [`osv-results.json`](osv-results.json)) |

| Пакет | Версия | Advisory | Манифесты | Почему остаётся |
|---|---|---|---|---|
| `golang.org/x/crypto` | 0.52.0 | `GO-2026-5932` | `go/orm-gorm-vs-jet`, `messaging/nats/clients/go` | про подпакет `openpgp`, который стенды **не импортируют** → ложное на уровне модуля; severity в записи — `Unknown` |
| `com.fasterxml.jackson.core:jackson-databind` | 2.18.9 | `GHSA-5jmj-h7xm-6q6v` | `databases/opensearch/ingestion/clients/java` | расхождение баз: Dependabot считает 2.18.9 фиксом, а в OSV на старом координате нет `fixed`-событий (фикс только на `tools.jackson.core` 3.x) |

## Как читать osv-results.json: смешанный результат (presence + Go-reachability)

Честно: `osv-results.json` — **не чистый presence**. Команда, которой он создан, — `osv-scanner scan --recursive --format json <repo>` (без `--call-analysis`), но `osv-scanner 2.4.0` для **собирающихся Go-модулей** делает анализ достижимости **по умолчанию** и кладёт в JSON поле `experimental_analysis.called`. Фактическая сводка файла:

- **3 вхождения** по манифестам;
- **2 уникальных** package/version (`x/crypto 0.52.0`, `jackson-databind 2.18.9`);
- **1 группа с результатом call-analysis**: `x/crypto` в `messaging/nats/clients/go` → `GO-2026-5932: {"called": false}` — модуль собирается, osv определил, что `openpgp` не вызывается. У `x/crypto` в `go/orm-gorm-vs-jet` анализа нет (модуль не собирается без codegen) → presence; у jackson — Maven, presence.

То есть в человекочитаемой таблице `osv-scanner` прячет `called:false`, а в JSON — оставляет с аннотацией; поэтому `x/crypto` виден в JSON дважды (nats + orm), а в таблице — один раз (только orm).

### openpgp — корректная presence-находка, а не «ошибка сканера»

[`govulncheck-nats.txt`](govulncheck-nats.txt) (`govulncheck -show verbose ./...` в `messaging/nats/clients/go`) кладёт `GO-2026-5932` в секцию **«Module Results»** — «modules you require, but your code doesn't appear to call these vulnerabilities». Важная формулировка: presence-сканер сработал **правильно** — уязвимый модуль `golang.org/x/crypto` в дереве действительно есть. Это **не ложное срабатывание**, а корректная presence-находка, **применимость которой снижает контекст приложения**: подпакет `openpgp` не вызывается, поэтому call-graph (`govulncheck`) её не считает достижимой. Разница важна: сканер не ошибся — решение «можно понизить приоритет» принимаете вы, зная, что символ недостижим.

### Тот же артефакт честно показывает 3 ДОСТИЖИМЫЕ stdlib-уязвимости

`govulncheck-nats.txt` в секции **«Symbol Results»** сообщает, что код **достижимо** затронут тремя уязвимостями стандартной библиотеки **Go 1.26.3** (это toolchain снимка):

| ID | Пакет stdlib | Фикс |
|---|---|---|
| `GO-2026-5856` | `crypto/tls` (утечка приватности в Encrypted Client Hello) | go1.26.5 |
| `GO-2026-5039` | `net/textproto` (неэкранированный ввод в ошибках) | go1.26.4 |
| `GO-2026-5037` | `crypto/x509` (неэффективный разбор hostname) | go1.26.4 |

Это **не** про зависимости: dependency-remediation в снимке завершена (Dependabot 0), но **стандартная библиотека Go 1.26.3 требует обновления до 1.26.5**. Урок ровно в этом: **SCA манифестов зависимостей не заменяет проверку stdlib/toolchain** — их ловит `govulncheck` по версии `go`, а не osv-scanner/Dependabot по манифестам. Полное «в ноль» здесь = бампы зависимостей **плюс** обновление Go. (`go/orm-gorm-vs-jet` для такого анализа требует предварительного codegen — см. README самого стенда.)

## Оговорка про приватную часть

Полный «путь 329 → 0» в статье включал и приватный `digital-cookbook-staging` (transitive Maven `netty`/`jackson`, Rust `rsa` и т.д.), недоступный читателю. Воспроизводимая **публичная** часть — этот снимок; приватные цифры в статье помечены как иллюстративные.
