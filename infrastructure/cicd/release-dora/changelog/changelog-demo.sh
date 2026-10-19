#!/usr/bin/env bash
# Раздел release-dora: changelog и следующая semver-версия из conventional
# commits. Показывает контракт «сообщения коммитов -> версия и changelog»:
#   - по коммитам после тега v1.2.0 git-cliff вычисляет следующую версию;
#   - breaking-коммит (feat!) поднимает MAJOR: ожидаем v2.0.0, не v1.3.0;
#   - неконвенциональный коммит в changelog не попадает и версию не двигает.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

export PATH="$PATH:$HOME/.cargo/bin"
if ! command -v git-cliff >/dev/null 2>&1; then
    echo "ОШИБКА: git-cliff не найден." >&2
    echo "Установи: cargo install git-cliff  (или скачай бинарь релиза git-cliff)." >&2
    exit 1
fi
echo "git-cliff $(git-cliff --version)"

echo
echo "=== сборка репозитория-фикстуры ==="
bash ./seed-repo.sh
REPO=".repo-fixture"

echo
echo "=== РАБОЧИЙ ВАРИАНТ: следующая версия по коммитам после v1.2.0 ==="
BUMPED="$(git-cliff --config cliff.toml --repository "$REPO" --bumped-version 2>/dev/null)"
echo "git-cliff вычислил следующую версию: ${BUMPED}"
echo "(v1.2.0 + breaking-коммит feat! => MAJOR => v2.0.0)"

echo
echo "=== changelog для невыпущенных изменений ==="
git-cliff --config cliff.toml --repository "$REPO" --unreleased --tag "$BUMPED" 2>/dev/null | tee CHANGELOG.generated.md

echo
echo "=== ПАДАЮЩИЙ ВАРИАНТ: неконвенциональный коммит ==="
# Добавляем коммит без conventional-префикса и показываем, что его нет в
# changelog: сообщение отбрасывается фильтром, дисциплина сообщений — контракт.
( cd "$REPO"
  echo "случайная правка" >> CHANGELOG_SOURCE.txt
  git add -A
  GIT_AUTHOR_DATE="2026-02-05T10:00:00" GIT_COMMITTER_DATE="2026-02-05T10:00:00" \
      git commit -q -m "почини там по-быстрому" )
NEW_CHANGELOG="$(git-cliff --config cliff.toml --repository "$REPO" --unreleased --tag "$BUMPED" 2>/dev/null)"

if printf '%s' "$NEW_CHANGELOG" | grep -q "по-быстрому"; then
    echo "ОШИБКА: неконвенциональный коммит попал в changelog — фильтр не работает." >&2
    exit 1
fi
echo "неконвенциональный коммит «почини там по-быстрому» в changelog НЕ попал — верно."

echo
echo "=== самопроверки ==="
if [ "$BUMPED" != "v2.0.0" ]; then
    echo "ОШИБКА: ожидалась версия v2.0.0 (breaking-коммит), получено ${BUMPED}." >&2
    exit 1
fi
echo "OK: breaking-коммит поднял версию до ${BUMPED}; неконвенциональные сообщения отфильтрованы."
