#!/usr/bin/env bash
# Статический гейт стенда: проверки, не требующие поднятого стенда.
# Гоняется перед сдачей и после любой правки.
set -uo pipefail
STAND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${STAND_DIR}"
fail=0

say() { printf '  %-50s %s\n' "$1" "$2"; }

# 1. Exec-бит скриптов В ИНДЕКСЕ git: права в рабочем каталоге под
#    Windows ничего не гарантируют.
#    Проверяем ТОЛЬКО *.sh: в scripts/ лежит ещё и сводка сквозного
#    прогона — файл данных, которому exec-бит не нужен.
bad="$(git ls-files -s 'scripts/*.sh' | grep -v '^100755' | awk '{print $4}' | tr '\n' ' ')"
if [[ -n "${bad}" ]]; then say "exec-бит скриптов" "НЕТ у: ${bad}"; fail=1
else say "exec-бит скриптов" "ок"; fi

# 2. Ни один compose не должен тянуть latest.
if grep -rn 'image:.*latest' compose/ >/dev/null 2>&1; then
    say "теги образов" "есть latest — версии не зафиксированы"; fail=1
else say "теги образов" "все пины явные"; fi

# 3. Имя проекта compose обязательно.
if grep -q '^name: temporal-cookbook' compose/compose.yml; then
    say "name в compose" "ок"
else say "name в compose" "отсутствует"; fail=1; fi

# 4. FIXTURES не должен содержать заготовок.
if [[ ! -s FIXTURES.md ]]; then
    say "FIXTURES.md" "отсутствует или пуст"; fail=1
elif grep -nE 'TODO|TBD|<\.\.\.>|XXX' FIXTURES.md >/dev/null 2>&1; then
    say "FIXTURES без заготовок" "есть незаполненные места"; fail=1
else say "FIXTURES без заготовок" "ок"; fi

# 5. Сырые прогоны профилей.
#
#    Логи прогонов НЕ хранятся в репозитории: в корне действует общее
#    правило `*.log`, и ни один стенд репозитория сырые логи не коммитит.
#    Поэтому на свежем клоне каталога .runs просто нет — и требовать его
#    наличия означало бы написать проверку, которая ни у кого, кроме
#    автора, пройти не может.
#
#    Здесь проверяется другое: если прогоны ЕСТЬ, они должны быть полными.
#    Ровно эта ситуация опасна — когда часть профилей прогнали, часть нет,
#    а FIXTURES собирают как будто по всем.
if [[ ! -d scripts/.runs ]]; then
    say "сырые прогоны профилей" "нет локально (норма для клона)"
else
    missing=0
    for p in 00-paradigm 01-internals 02-determinism 03-activities \
             04-messaging 05-versioning 06-operations 07-languages; do
        if [[ ! -s "scripts/.runs/${p}.log" ]]; then
            echo "    нет прогона: ${p}"; missing=1
        fi
    done
    if (( missing )); then say "сырые прогоны восьми профилей" "НЕПОЛНЫ"; fail=1
    else say "сырые прогоны восьми профилей" "все восемь на месте"; fi
fi

# 6. Запрещённое в статьях слово не используется и в стенде.
#    Сам файл проверки исключён: иначе он находит собственный шаблон и
#    падает всегда — проверка, которая не может пройти, бесполезна.
if grep -rni 'грабл' README.md FIXTURES.md scripts/ go/ clients/ \
       --exclude=verify-static.sh >/dev/null 2>&1; then
    say "лексика" "встречается запрещённое слово"; fail=1
else say "лексика" "ок"; fi

# 7. Версии SDK в манифестах не должны быть плавающими.
floating=0
grep -q '"@temporalio/worker": "[0-9]' clients/ts/package.json || floating=1
grep -qE '^temporalio==[0-9]' clients/python/requirements.txt || floating=1
grep -q '<temporal.version>[0-9]' clients/java/pom.xml || floating=1
grep -q 'Include="Temporalio" Version="[0-9]' clients/dotnet/Worker.csproj || floating=1
if (( floating )); then say "версии SDK в манифестах" "есть плавающие"; fail=1
else say "версии SDK в манифестах" "все пины явные"; fi

# 8. Go собирается и проходит vet.
#    Причину провала ПЕЧАТАЕМ. Заглушённый вывод превращает любую ошибку
#    окружения — например, запуск из Git Bash под Windows, где путь
#    монтирования Docker мангается, — в неотличимое «провал», и человек
#    идёт искать несуществующую ошибку в коде.
if go_out="$(docker run --rm -v "${STAND_DIR}/go:/src" -v temporal-cookbook-gocache:/gocache -w /src \
    -e GOFLAGS=-mod=mod -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build \
    -e GOPROXY="https://go.khorost.tech,direct" \
    golang:1.26.3-alpine sh -c 'go build ./... && go vet ./...' 2>&1)"; then
    say "go build + vet" "ок"
else
    say "go build + vet" "провал"
    echo "${go_out}" | head -3 | sed 's/^/      /'
    fail=1
fi

echo
if (( fail )); then echo "verify-static: ЕСТЬ ПРОБЛЕМЫ"; exit 1; fi
echo "verify-static: чисто"
