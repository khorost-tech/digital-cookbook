#!/usr/bin/env bash
# Раздел zero-downtime, уровень B (k8s-volga). РАБОЧИЙ ВАРИАНТ: тот же выкат
# v1 -> v2, но через expand-contract — ни один под не уходит в NotReady в окне
# rolling update.
#
# Порядок «аддитивное до кода, деструктивное после» на настоящем выкате:
#   1. EXPAND до выката: ADD COLUMN total_cents (nullable), снять NOT NULL с
#      amount_cents. Обе колонки на месте — /healthz и v1 (amount), и v2 (total)
#      проходит. Схема совместима с ОБОИМИ образами одновременно.
#   2. BACKFILL старых строк.
#   3. Выкат ПРОМЕЖУТОЧНОЙ версии (образ :dual — пишет ОБЕ колонки, читает
#      новую). Именно она делает откат безопасным: пока она работает, обе
#      колонки заполнены, и соседняя версия в любую сторону найдёт своё.
#   4. ДОБОР backfill: строки, записанные v1-подами во время выката, остались с
#      total_cents IS NULL. Без добора следующий шаг потерял бы эти значения.
#   5. Выкат образа v2 (только новая колонка) поверх работающего dual.
#   6. CONTRACT: когда ни v1, ни dual не осталось, удаляем amount_cents.
#   7. Восстановление NOT NULL на total_cents — чтобы итоговая схема совпадала
#      с той, к которой приводит наивный выкат. Проверяется в конце скрипта.
#
# ⚠️ Про откат. Двухступенчатый выкат (v1 -> v2, без dual) структурно позволяет
# вернуть предыдущий образ, но данные к этому моменту уже неполны: строки,
# записанные v2, не имеют amount_cents, и откатившийся v1 их не увидит. Поэтому
# промежуточная версия здесь не украшение, а условие обещания «откат безопасен».
# Скрипт это проверяет: перед CONTRACT считает строки, которые не увидела бы
# откатившаяся старая версия, и падает, если такие есть.
#
# Граница честности: безопасен откат НА ОДНУ СТУПЕНЬ и только ДО contract. После
# DROP COLUMN вернуться к версии, читающей amount_cents, нельзя — колонки уже нет,
# и это восстановление из резервной копии, а не откат.
#
# ⚠️ Требует доступ к k8s-volga (KUBECONFIG_VOLGA) и образы IMAGE_V1/IMAGE_V2.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

# Промежуточный образ нужен только этому сценарию, поэтому требование стоит
# здесь, а не в lib.sh: иначе naive-rollout-demo.sh падал бы на загрузке
# библиотеки, хотя ему этот образ не нужен.
: "${IMAGE_DUAL:?переменная IMAGE_DUAL не задана (образ промежуточной версии, SCHEMA=dual)}"

require_volga

echo "=== чистый старт: удаляем прежний namespace, если остался ==="
kubectl delete ns "$NS" --ignore-not-found --wait=true --timeout=120s

echo "=== развёртывание: namespace, PostgreSQL, приложение v1 (3 реплики) ==="
kubectl apply -f namespace.yaml
ensure_pull_secret
kubectl apply -f postgres.yaml
kubectl -n "$NS" rollout status deployment/postgres --timeout=120s
apply_tpl app.yaml
wait_rollout
echo "стартовое состояние: НЕготовых подов приложения = $(not_ready_app_pods) (ожидаем 0)"

echo
echo "=== шаг 1: EXPAND (до выката) — ADD COLUMN total_cents, снять NOT NULL с amount_cents ==="
psql_ddl "ALTER TABLE orders ADD COLUMN total_cents integer"
psql_ddl "ALTER TABLE orders ALTER COLUMN amount_cents DROP NOT NULL"

echo "=== шаг 2: BACKFILL старых строк ==="
psql_cluster -c "UPDATE orders SET total_cents = amount_cents WHERE total_cents IS NULL" >/dev/null

trap cleanup_watch_files EXIT

echo
echo "=== выкат 1 (v1 -> dual): следим за регрессией уходящих v1-подов ==="
watch_regressions "$IMAGE_V1"

echo "--- шаг 3: выкат ПРОМЕЖУТОЧНОЙ версии (dual: пишет обе колонки) ---"
kc set image deploy/zdt-app app="$IMAGE_DUAL"
wait_rollout
stop_watch
MAX_V1="$REGRESSIONS"
echo "максимум регрессировавших v1-подов: ${MAX_V1}"

echo "--- шаг 4: ДОБОР backfill (строки v1-подов, записанные во время выката) ---"
# v1-поды писали только amount_cents всё время своего выката, и их строки
# остались с total_cents IS NULL. Без добора CONTRACT потерял бы эти значения.
#
# Дефект был найден проверкой постусловия, добавленной по ревью: VALIDATE
# CONSTRAINT упал с «check constraint ... is violated by some row» именно на
# этих строках. До появления проверки сценарий молча заканчивался «успехом».
LEFT="$(psql_cluster -t -A -c "SELECT count(*) FROM orders WHERE total_cents IS NULL" | tr -d '\r')"
echo "строк с total_cents IS NULL после выката v1->dual: ${LEFT}"
psql_cluster -c "UPDATE orders SET total_cents = amount_cents WHERE total_cents IS NULL" >/dev/null

# Дать промежуточной версии поработать: она пишет ОБЕ колонки, поэтому с этого
# момента данные полны для обеих сторон отката.
sleep 5

# ТОЧКА 1 ПРОВЕРКИ ОТКАТА: пока работает dual, ни одной строки без amount_cents
# быть не должно — значит откат на v1 в этот момент безопасен по данным.
BLIND_DUAL="$(psql_cluster -t -A -c "SELECT count(*) FROM orders WHERE amount_cents IS NULL" | tr -d '\r')"
echo "строк без amount_cents во время работы dual: ${BLIND_DUAL} (откат на v1 безопасен при 0)"

echo "=== выкат 2 (dual -> v2): следим за регрессией уходящих DUAL-подов ==="
watch_regressions "$IMAGE_DUAL"

echo "--- шаг 5: выкат v2 (только новая колонка) поверх работающего dual ---"
kc set image deploy/zdt-app app="$IMAGE_V2"
wait_rollout
stop_watch
MAX_DUAL="$REGRESSIONS"
echo "максимум регрессировавших dual-подов: ${MAX_DUAL}"
sleep 3

# ТОЧКА 2: после выката v2 строки без amount_cents появляются — v2 её не пишет.
# Это и есть граница обещания: откат на СОСЕДНЮЮ версию (dual читает
# total_cents) безопасен, а «через ступень», на v1, — уже нет. Считаем явно,
# чтобы не выдавать желаемое за действительное.
BLIND="$(psql_cluster -t -A -c "SELECT count(*) FROM orders WHERE amount_cents IS NULL" | tr -d '\r')"
echo "строк без amount_cents после выката v2: ${BLIND} (их не увидел бы откат на v1)"

echo "--- шаг 6: CONTRACT — удаляем amount_cents (ни v1, ни dual не осталось) ---"
psql_ddl "ALTER TABLE orders DROP COLUMN amount_cents"

echo "--- шаг 7: восстановить NOT NULL на total_cents ---"
# Без этого шага сценарий заканчивался бы ОСЛАБЛЕННОЙ схемой: исходная колонка
# была NOT NULL, а expand добавляет nullable. Последовательность та же, что в
# локальном демо: дорогой скан идёт под SHARE UPDATE EXCLUSIVE, а под
# ACCESS EXCLUSIVE остаётся операция без скана.
psql_ddl "ALTER TABLE orders ADD CONSTRAINT total_cents_not_null CHECK (total_cents IS NOT NULL) NOT VALID"
# VALIDATE берёт SHARE UPDATE EXCLUSIVE — чтение и запись не блокирует,
# поэтому lock_timeout ему не нужен; дорогой скан таблицы идёт именно здесь.
psql_cluster -c "ALTER TABLE orders VALIDATE CONSTRAINT total_cents_not_null" >/dev/null
psql_ddl "ALTER TABLE orders ALTER COLUMN total_cents SET NOT NULL"
psql_ddl "ALTER TABLE orders DROP CONSTRAINT total_cents_not_null"

echo
echo "=== ИТОГ ==="
echo "регрессировавших v1-подов в выкате 1 (v1->dual):   ${MAX_V1}"
echo "регрессировавших dual-подов в выкате 2 (dual->v2): ${MAX_DUAL}"
echo
echo "Каждый выкат проверялся по СВОЕЙ уходящей версии, а не по одному образу —"
echo "иначе второй выкат давал бы неизбежный ноль и ничего не доказывал."
echo
echo "Сравнение с naive-rollout-demo.sh: там несовместимая миграция роняет"
echo "readiness ещё живых старых подов; здесь схема весь переход совместима с"
echo "обеими работающими версиями, и окна несовместимости нет ни на одном выкате."

# Проверка эквивалентности схемы — до уборки namespace, пока БД ещё жива.
# Наивный выкат приходит к total_cents NOT NULL; сюда должны прийти так же,
# иначе сравнение двух сценариев нечестно (та же проверка есть в локальном демо).
SCHEMA="$(psql_cluster -t -A -c "SELECT column_name||':'||is_nullable FROM information_schema.columns
                                 WHERE table_name='orders' AND column_name IN ('amount_cents','total_cents')
                                 ORDER BY column_name" | tr -d '\r' | paste -sd, -)"
echo "итоговая схема: ${SCHEMA}"

echo "--- уборка namespace ---"
cleanup_ns

if [ "${MAX_V1:-0}" -ne 0 ] || [ "${MAX_DUAL:-0}" -ne 0 ]; then
    echo "ОШИБКА: в окне выката регрессировали поды (v1=${MAX_V1}, dual=${MAX_DUAL}) — переход НЕ бесшовный." >&2
    exit 1
fi
if [ "$SCHEMA" != "total_cents:NO" ]; then
    echo "ОШИБКА: итоговая схема '${SCHEMA}' не совпадает с результатом наивного выката." >&2
    echo "Ожидалось ровно 'total_cents:NO' (колонка есть и она NOT NULL)." >&2
    exit 1
fi
# Обещание про откат проверяется числом, а не декларацией: пока работала
# промежуточная версия, данные обязаны быть полны для ОБЕИХ сторон.
if [ "${BLIND_DUAL:-1}" -ne 0 ]; then
    echo "ОШИБКА: во время работы dual нашлось ${BLIND_DUAL} строк без amount_cents." >&2
    echo "Значит откат на предыдущую версию потерял бы данные — обещание не выполнено." >&2
    exit 1
fi
echo "OK: expand-contract выкат прошёл без единого НЕготового пода, схема эквивалентна,"
echo "во время работы промежуточной версии откат на предыдущую был безопасен (0 строк без amount_cents)."
