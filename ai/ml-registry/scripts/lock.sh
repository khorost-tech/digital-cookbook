#!/usr/bin/env bash
# Пересобрать requirements*.txt из *.in: версии не новее отсечки, с хешами.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
docker run --rm -v "$PWD":/w -w /w python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1 \
  sh -c 'pip install -q uv==0.12.5 && for f in requirements.in envs/*.in; do
    uv pip compile "$f" --generate-hashes --python-version 3.12 --python-platform linux --exclude-newer 2026-10-03 \
      --no-header -o "${f%.in}.txt"; done'
