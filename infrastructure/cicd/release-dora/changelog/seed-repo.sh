#!/usr/bin/env bash
# Строит репозиторий-фикстуру .repo-fixture/ с детерминированной историей
# conventional commits: несколько релизов, тег v1.2.0, затем набор коммитов,
# среди которых есть breaking (feat!). changelog-demo.sh считает по нему
# следующую версию — она должна стать v2.0.0 именно из-за breaking-коммита.
#
# Даты коммитов ЗАДАНЫ явно (не текущее время), чтобы фикстура и все числа были
# воспроизводимы между прогонами.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

REPO=".repo-fixture"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"

git init -q -b main
git config user.name "cicd-stand"
git config user.email "stand@example.com"

# Детерминированный коммит с заданной датой.
commit() {
    local msg="$1" date="$2"
    echo "$msg @ $date" >> CHANGELOG_SOURCE.txt
    git add -A
    GIT_AUTHOR_DATE="$date" GIT_COMMITTER_DATE="$date" git commit -q -m "$msg"
}

# --- история до релиза v1.2.0 ---
commit "feat: первичная модель заказа"                 "2026-01-05T10:00:00"
commit "feat: список заказов в API"                    "2026-01-06T10:00:00"
commit "fix: пустой список не роняет обработчик"        "2026-01-07T10:00:00"
git tag v1.2.0

# --- коммиты ПОСЛЕ v1.2.0 (по ним считается следующая версия) ---
commit "feat: фильтр заказов по статусу"               "2026-02-01T10:00:00"
commit "fix: корректная пагинация на последней странице" "2026-02-02T10:00:00"
commit "docs: описание эндпоинтов заказов"             "2026-02-03T10:00:00"
# Breaking change: переименование поля в ответе API. Именно он должен поднять
# MAJOR (v1.2.0 -> v2.0.0), а не MINOR.
commit "feat!: переименовать amount в total в ответе API" "2026-02-04T10:00:00"

echo "Фикстура собрана в $(pwd)"
echo "Тег: $(git describe --tags --abbrev=0)"
echo "Коммитов после тега: $(git rev-list --count v1.2.0..HEAD)"
