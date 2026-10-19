#!/usr/bin/env bash
# Раздел github-actions: валидация workflow через actionlint.
#
# Что здесь показано. actionlint статически проверяет workflow: синтаксис
# YAML, доступность контекстов (${{ ... }}), корректность needs, версии
# actions, shell-скрипты внутри run (через shellcheck). Ошибку в выражении,
# которую ни глаз в ревью, ни `hugo build` статьи не заметят, actionlint
# ловит до того, как workflow впервые запустится в облаке и потратит минуты.
#
# Скрипт делает две вещи:
#   1. гоняет actionlint на реальных workflow стенда — они должны быть чисты;
#   2. падающий вариант: создаёт временный workflow с битым выражением и
#      проверяет, что actionlint его ЛОВИТ. Если не поймал — валидация не
#      работает, и это провал демонстрации (exit 1).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."   # -> github-actions/

# actionlint резолвит локальные ./.github/actions и reusable workflow относительно
# переданных файлов, поэтому запускаемся из github-actions/ и передаём пути явно
# (без аргументов actionlint ищет .github/workflows от git-корня репозитория —
# а наш пример лежит в подкаталоге стенда, не в корне).
export PATH="$PATH:$HOME/go/bin"
if ! command -v actionlint >/dev/null 2>&1; then
    echo "actionlint не найден — ставлю через go install..." >&2
    GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}" \
        go install github.com/rhysd/actionlint/cmd/actionlint@latest
fi

echo "actionlint $(actionlint --version | head -1)"

mapfile -t WF < <(find .github/workflows -maxdepth 1 -name '*.yml' | sort)
if [ "${#WF[@]}" -eq 0 ]; then
    echo "ОШИБКА: не найдено ни одного workflow в .github/workflows/" >&2
    exit 1
fi

echo
echo "=== РАБОЧИЙ ВАРИАНТ: реальные workflow стенда ==="
printf '  %s\n' "${WF[@]}"
if actionlint -color "${WF[@]}"; then
    echo "actionlint: чисто — все workflow валидны."
else
    echo "ОШИБКА: actionlint нашёл проблемы в РЕАЛЬНЫХ workflow стенда." >&2
    echo "Это дефект стенда, а не демонстрация — чини workflow." >&2
    exit 1
fi

echo
echo "=== ПАДАЮЩИЙ ВАРИАНТ: workflow с битым выражением ==="
# Кладём временный файл рядом с настоящими (в .github/workflows/), чтобы
# резолвинг локальных ссылок работал так же, как для настоящих. trap удаляет
# его при любом выходе — файл не должен ни закоммититься, ни остаться после
# прерывания.
BROKEN="$(mktemp .github/workflows/broken-demo.XXXXXX.yml)"
trap 'rm -f "$BROKEN"' EXIT
cat > "$BROKEN" <<'YAML'
name: broken-demo
on: push
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      # Контекст inputs наполняют только workflow_call и workflow_dispatch,
      # и только объявленными параметрами. У триггера push их нет —
      # человеческий глаз это пропускает, actionlint нет.
      - run: echo "${{ inputs.nonexistent }}"
YAML

echo "Битый файл: $BROKEN (обращение к недоступному контексту inputs)"
if actionlint -color "$BROKEN"; then
    echo "ОШИБКА: actionlint НЕ поймал заведомо битое выражение." >&2
    echo "Значит валидация не работает — провал демонстрации." >&2
    exit 1
else
    echo
    echo "actionlint правильно отверг битый workflow (ненулевой код выше)."
fi

echo
echo "ИТОГ: реальные workflow валидны, заведомо битый — отвергнут. Валидация работает."
