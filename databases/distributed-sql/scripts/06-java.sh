#!/usr/bin/env bash
#
# 06-java.sh <crdb|yb> — retry-цикл сериализуемых переводов на JDBC.
# Иллюстрация паттерна, а не замер: числа отсюда с Go не сравниваются.
#
#   bash scripts/06-java.sh crdb   → fixtures/06-java-crdb.txt

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"
case "$e" in crdb|yb) ;; *) fail "только PG-wire кластеры: crdb, yb" ;; esac
require_running "$e"
f="06-java-$e"
fixture_header "$f" "$e"

# Кэш Maven — именованный том, чтобы зависимости не качались каждый раз.
for mode in update rmw; do
    capture "$f" "4 потока × 25 переводов, SERIALIZABLE, режим $mode" \
        "docker run --rm --network dsql -v \"\$PWD/javaretry:/app\" -v dsql-m2:/root/.m2 -w /app maven:3.9-eclipse-temurin-21 mvn -q compile exec:java -Dexec.args='$e $mode 4 25'"
done
log "записано: $FIXTURES_DIR/$f.txt"
