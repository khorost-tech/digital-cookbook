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
osv-scanner scan --recursive /tmp/dc-src > osv-repro.json
# сравнить osv-repro.json с osv-results.json (снят на том же исходнике)

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

## Presence vs call-analysis (отсев openpgp)

`osv-results.json` — **чистый presence-скан**: `osv-scanner` по умолчанию call-analysis **не** запускает (в JSON нет `experimentalAnalysis`), он включается **явным** флагом `--call-analysis=<lang>`.

Что call-graph реально отсекает `openpgp` — в [`govulncheck-nats.txt`](govulncheck-nats.txt): модуль `messaging/nats/clients/go` тянет `golang.org/x/crypto v0.52.0` (в затронутом диапазоне `GO-2026-5932`), но `govulncheck` эту запись **не показывает** — openpgp не вызывается; в выводе только достижимые stdlib-уязвимости. Эквивалент через osv-scanner (для собирающихся модулей):

```bash
osv-scanner scan --call-analysis=go ./messaging/nats/clients/go
```

(`go/orm-gorm-vs-jet` для call-analysis требует предварительного codegen — см. README самого стенда.)

## Оговорка про приватную часть

Полный «путь 329 → 0» в статье включал и приватный `digital-cookbook-staging` (transitive Maven `netty`/`jackson`, Rust `rsa` и т.д.), недоступный читателю. Воспроизводимая **публичная** часть — этот снимок; приватные цифры в статье помечены как иллюстративные.
