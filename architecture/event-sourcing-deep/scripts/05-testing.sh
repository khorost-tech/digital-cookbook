#!/usr/bin/env bash
#
# 05-testing.sh — артефакт к статье «Тесты для событийной модели: given-
# when-then, replay и контраст с фреймворком».
#
# Прогоняет ДВА независимых набора тестов над одним и тем же доменом
# (резервуар/кислород):
#   1. go/*_test.go — включая replay-тест (TestReplayAgainstRecordedHistory),
#      который проигрывает go/testdata/history.jsonl текущим Tank.Apply и
#      сверяет итог с golden-файлом go/testdata/expected.json. Ценность
#      этого теста доказана НЕ прогоном скрипта, а отдельной процедурой
#      (см. FIXTURES.md, «Артефакт 05, шаг 3»): в код временно вносилась
#      регрессия, тест ловил её (FAIL), затем регрессия убиралась и тест
#      снова проходил (PASS) — то есть тест умеет падать, а не только
#      проходить.
#   2. java/ — тот же агрегат на Axon Framework (TankAggregate), тесты
#      given-when-then через AggregateTestFixture (TankAggregateTest).
#
# Постусловие: ОБА набора обязаны быть зелёными. Падение любого из них —
# дефект стенда, а не результат для статьи.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/05-testing.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-05
mkdir -p "$OUT"

# Локальный репозиторий Maven по умолчанию в WSL (~/.m2/repository)
# принадлежит root — сборка падает с AccessDenied. Обходим отдельным
# репозиторием; переопределяемо через M2_REPO для CI с другим окружением.
M2_REPO="${M2_REPO:-/tmp/m2-esdeep}"
mkdir -p "$M2_REPO"

echo "=== 1/2: go test ./... (включая replay-тест против зафиксированной истории) ==="
GO_START=$(date +%s.%N)
( cd go && go test -v ./... ) | tee "$OUT/go-test.log"
GO_END=$(date +%s.%N)
GO_TESTS=$(grep -c '^--- PASS' "$OUT/go-test.log" || true)
GO_SECS=$(python3 -c "print(f'{$GO_END - $GO_START:.2f}')")
[ "$GO_TESTS" -gt 0 ] || fail "go test: ни один тест не отчитался PASS — лог: $OUT/go-test.log"
grep -q '^ok' "$OUT/go-test.log" || fail "go test: пакет не завершился 'ok' — смотри $OUT/go-test.log"
log "go: $GO_TESTS тестов PASS за ${GO_SECS}с"

echo
echo "=== 2/2: mvn -q test (Axon: TankAggregate, given-when-then) ==="
JAVA_START=$(date +%s.%N)
( cd java && mvn -Dmaven.repo.local="$M2_REPO" test ) | tee "$OUT/mvn-test.log"
JAVA_END=$(date +%s.%N)
JAVA_SECS=$(python3 -c "print(f'{$JAVA_END - $JAVA_START:.2f}')")
grep -q 'BUILD SUCCESS' "$OUT/mvn-test.log" || fail "mvn test: BUILD SUCCESS не найден — смотри $OUT/mvn-test.log"
JAVA_SUMMARY="$(grep -E '^\[INFO\] Tests run:' "$OUT/mvn-test.log" | tail -1)"
[ -n "$JAVA_SUMMARY" ] || fail "mvn test: итоговая строка 'Tests run:' не найдена"
JAVA_FAILURES="$(echo "$JAVA_SUMMARY" | sed -n 's/.*Failures: \([0-9]*\).*/\1/p')"
JAVA_ERRORS="$(echo "$JAVA_SUMMARY" | sed -n 's/.*Errors: \([0-9]*\).*/\1/p')"
[ "$JAVA_FAILURES" = "0" ] && [ "$JAVA_ERRORS" = "0" ] \
    || fail "mvn test: есть провалы ($JAVA_SUMMARY)"
log "java/Axon: $JAVA_SUMMARY, за ${JAVA_SECS}с"

echo
echo "=== Постусловие ==="
log "оба набора зелёные: go — $GO_TESTS тестов PASS (${GO_SECS}с),"
log "java/Axon — $JAVA_SUMMARY (${JAVA_SECS}с)"
