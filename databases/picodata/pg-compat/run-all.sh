#!/usr/bin/env bash
# run-all.sh — прогоняет один набор проб всеми клиентами и собирает RESULTS.md.
#
# Кластер должен быть уже поднят: bash ../ops/up.sh 0.2.0
# Смысл пяти клиентов — отделить «сервер не умеет» от «драйвер не умеет».
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
DIR_DOCKER="$(cd "$(dirname "$0")" && (pwd -W 2>/dev/null || pwd))"
cd "$DIR"
mkdir -p out
export MSYS_NO_PATHCONV=1

PY="$(command -v python3 || command -v python)"

echo "=== psql ==="
bash runners/psql.sh probes.tsv > out/psql.tsv

echo "=== pgx ==="
( cd runners/pgx && go run . -probes ../../probes.tsv ) > out/pgx.tsv

echo "=== psycopg ==="
docker build -q -t pg-compat-psycopg runners/psycopg/ >/dev/null
docker run --rm --network picodata-net \
    -v "${DIR_DOCKER}/probes.tsv:/probes.tsv:ro" \
    pg-compat-psycopg /probes.tsv > out/psycopg.tsv

echo "=== JDBC PostgreSQL ==="
bash runners/jdbc/run.sh postgresql > out/jdbc-postgresql.tsv

echo "=== JDBC Picodata ==="
bash runners/jdbc/run.sh picodata > out/jdbc-picodata.tsv

echo "=== метаданные JDBC (оба драйвера) ==="
bash runners/jdbc/run.sh postgresql --metadata > out/metadata-jdbc-postgresql.tsv
bash runners/jdbc/run.sh picodata   --metadata > out/metadata-jdbc-picodata.tsv

# Матрица выше отвечает только на вопрос «принят ли синтаксис». Отдельным шагом
# проверяем то, чего она не проверяет: совпадают ли РЕЗУЛЬТАТЫ с PostgreSQL и
# выполняются ли постусловия DML. Без этого «работает» означало бы «не отвергнуто».
echo "=== сравнение результатов с PostgreSQL ==="
bash results.sh || { echo "!!! результаты расходятся — матрица недействительна" >&2; exit 1; }

echo "=== сводка ==="
"$PY" report.py probes.tsv \
    psql=out/psql.tsv \
    pgx=out/pgx.tsv \
    psycopg=out/psycopg.tsv \
    "JDBC PostgreSQL=out/jdbc-postgresql.tsv" \
    "picodata-jdbc=out/jdbc-picodata.tsv" > RESULTS.md

{
    echo
    echo "## Метаданные JDBC"
    echo
    echo "SQL-пробы у обоих драйверов совпадают, поэтому вопрос «зачем фирменный"
    echo "драйвер» решается не здесь, а на уровне \`DatabaseMetaData\` — того самого,"
    echo "по которому GUI-клиенты строят дерево объектов."
    echo
    echo "| Проба | JDBC PostgreSQL | picodata-jdbc | Значение у picodata-jdbc |"
    echo "|---|---|---|---|"
    paste out/metadata-jdbc-postgresql.tsv out/metadata-jdbc-picodata.tsv \
        | awk -F'\t' '{
            pg = ($2=="OK") ? "✅" : "❌";
            pico = ($5=="OK") ? "✅" : "❌";
            printf "| `%s` | %s | %s | %s |\n", $1, pg, pico, $6
        }'
} >> RESULTS.md

echo "готово: RESULTS.md"
