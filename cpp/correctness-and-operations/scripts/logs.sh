#!/usr/bin/env bash
#
# logs.sh — одна строка оси 6: собрать log_probe и напечатать запись лога.
#
#   bash scripts/logs.sh <pattern|escaped> <plain|quote|backslash|newline|byte-ff>
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
docker build -q -t cpp-correctness-and-operations-base:latest -f images/Dockerfile.base . >/dev/null
exec docker run --rm -v "$PWD:/src:ro" cpp-correctness-and-operations-base:latest bash -c \
    "g++ -std=c++23 -O2 /src/logs/log_probe.cpp -lspdlog -lfmt -o /tmp/l && /tmp/l '$1' '$2'"
