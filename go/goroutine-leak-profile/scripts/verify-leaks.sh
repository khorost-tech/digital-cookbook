#!/usr/bin/env bash
# Профиль goroutineleak обязан найти ровно столько утечек, сколько подняли
# (leaks.Total() — все 5 классов), и НЕ найти контрольный класс
# mutex-held-by-live-goroutine. cmd/leaks/main.go снимает профиль дважды
# (в прогонах этого стенда результат был полнее со второго WriteTo, см.
# комментарий в main.go и results/00-findings.txt) — runtime.GC() перед
# снятием здесь не нужен. Это ПРОВЕРКА: расхождение — отказ, а не строка
# в отчёте.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
mkdir -p results

if GOTOOLCHAIN=go1.27.0 go run ./cmd/leaks > results/01-profile.txt 2>&1; then
  echo "Отчёт: results/01-profile.txt"
  tail -3 results/01-profile.txt
else
  echo "ОТКАЗ: профиль нашёл не то число утечек — подробности в results/01-profile.txt" >&2
  tail -5 results/01-profile.txt >&2
  exit 1
fi
