#!/usr/bin/env bash
# psql.sh — прогоняет probes.tsv через psql (штатный клиент PostgreSQL 18.4).
# Печатает строки "<id>\t<OK|FAIL>\t<сообщение>".
#
# SQL подаётся психу на СТАНДАРТНЫЙ ВВОД (`psql -f -`), а не аргументом `-c`.
# Это принципиально: аргумент прошёл бы ещё один слой shell-квотинга, и
# одинарные кавычки внутри запроса ('too%') сменили бы смысл — на разведке
# проба LIKE именно так дала ложный FAIL «column with name "too%" not found».
# Через stdin запрос доезжает до сервера ровно таким, каким записан в probes.tsv.
set -uo pipefail

PROBES="${1:?usage: psql.sh <probes.tsv>}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"

while IFS=$'\t' read -r id group sql; do
    case "$id" in ''|\#*) continue ;; esac
    [ -n "${sql:-}" ] || continue

    out="$(printf '%s\n' "$sql" | MSYS_NO_PATHCONV=1 docker exec -i picodata-origin \
           psql "$PICO_DSN" -tA -v ON_ERROR_STOP=1 -f - 2>&1)"
    rc=$?

    msg="$(printf '%s' "$out" | tr '\n' ' ' | tr -s ' ' | cut -c1-160)"
    if [ "$rc" -eq 0 ]; then
        printf '%s\tOK\t%s\n' "$id" "$msg"
    else
        printf '%s\tFAIL\t%s\n' "$id" "$msg"
    fi
done < "$PROBES"
