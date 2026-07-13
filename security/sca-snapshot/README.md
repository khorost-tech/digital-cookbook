# SCA-снимок: воспроизводимый пример к статье

Приложение к статье [«Сканирование уязвимых зависимостей (SCA): Dependabot, osv-scanner, govulncheck»](https://khorost.tech/security/dependency-scanning-sca-dependabot-osv/).

Это **воспроизводимый снимок** состояния публичного репозитория `digital-cookbook` **после** ремедиации: показывает контраст «Dependabot видит 0 — osv-scanner видит остаток» и ровно два «краевых» кейса из статьи (ложное срабатывание на уровне модуля и расхождение диапазонов между базами).

## Снимок

- **Репозиторий:** `github.com/khorost-tech/digital-cookbook`
- **Commit:** `76ca0516c94865d6495c15910680bd6ebe910436`
- **Дата:** 2026-07-13
- **Инструменты:** osv-scanner 2.4.0 · govulncheck v1.6.0 · cargo-audit 0.22.2 · Go 1.26.3 (osv-scanner 2.4.0 требует Go ≥ 1.26.4 и при call-analysis сам переключается на новый toolchain, напр. 1.26.5) · Dependabot (GitHub Advisory DB)

## Как воспроизвести

```bash
git clone https://github.com/khorost-tech/digital-cookbook
cd digital-cookbook
git checkout 76ca0516c94865d6495c15910680bd6ebe910436

# Dependabot-счётчик (нужен gh auth) — ожидаемо 0
gh api -X GET repos/khorost-tech/digital-cookbook/dependabot/alerts \
  -f state=open --paginate | jq 'length'          # -> 0

# osv-scanner — остаток (см. osv-results.json в этой папке)
go install github.com/google/osv-scanner/v2/cmd/osv-scanner@latest
osv-scanner scan --recursive --format json . > osv-results.json
```

## Результат снимка

| Источник | Находки |
|---|---|
| **Dependabot** (GitHub Advisory DB) | **0** открытых алертов |
| **osv-scanner** (OSV) | **2 пакета**, оба — «краевые» кейсы статьи |

Два оставшихся у osv-scanner (полный вывод — [`osv-results.json`](osv-results.json)):

| Пакет | Версия | Advisory | Почему остаётся |
|---|---|---|---|
| `golang.org/x/crypto` | 0.52.0 | `GO-2026-5932` (+ связанное) | про подпакет `openpgp`, который стенды **не импортируют** → ложное на уровне модуля; `govulncheck` по call-graph это отсекает. severity в записи — `Unknown` |
| `com.fasterxml.jackson.core:jackson-databind` | 2.18.9 | `GHSA-5jmj-h7xm-6q6v` | расхождение баз: Dependabot считает 2.18.9 фиксом и алерт закрыл, а в OSV на старом координате нет `fixed`-событий (фикс только на `tools.jackson.core` 3.x) → osv-scanner продолжает флагать |

## Оговорка про приватную часть

Полный «путь 329 → 0» в статье включал и приватный `digital-cookbook-staging` (transitive Maven `netty`/`jackson`, Rust `rsa` и т.д.), недоступный читателю. Воспроизводимая **публичная** часть — этот снимок; приватные цифры в статье помечены как иллюстративные.
