#!/usr/bin/env bash
# Раздел release-dora: пять метрик DORA и разоблачение KPI-театра.
#
# Модель актуальная (DORA, 2024): throughput — deployment frequency, change lead
# time, failed deployment recovery time; instability — change fail rate,
# deployment rework rate. Прежние «четыре ключа» с MTTR устарели: MTTR в 2023
# переопределена как failed deployment recovery time (только отказы, вызванные
# изменением), а в 2024 добавлена deployment rework rate.
#
# Считаем метрики на честной фикстуре и на ДВУХ вариантах «театральной»: те же
# изменения, каждый деплой разбит надвое, но копии аварийного деплоя размечены
# по-разному.
#
#   вариант A — обе копии помечены rework;
#   вариант B — только одна (вторая считается плановой).
#
# Зачем два. Первая редакция демо утверждала, что deployment rework rate
# «устойчива по построению», потому что дробление делит и числитель, и
# знаменатель. Это верно ТОЛЬКО при разметке A. Определение метрики не
# предписывает, как классифицировать копии, — и при разметке B доля падает
# вдвое, ровно как change fail rate. Вывод об устойчивости оказался следствием
# выбранной классификации, а не свойством метрики; демо теперь показывает оба
# варианта, чтобы это было видно, а не подразумевалось.
#
# Что действительно устойчиво к ЭТОЙ стратегии накрутки в обоих вариантах:
#   change lead time                — коммиты те же;
#   failed deployment recovery time — инцидент тот же.
#
# ⚠️ Демо доказывает узкое: дробление деплоев обнаруживается несогласованностью
# метрик, опирающихся на разные события. Оно НЕ доказывает, что метрики вообще
# нельзя подделать: границу деплоя, классификацию rework, привязку инцидентов к
# релизам и момент фиксации коммита выбирает тот же, кто отчитывается.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

export GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}" GOSUMDB="${GOSUMDB:-off}"
BIN="$(mktemp)"
HONEST="$(mktemp)"
THEATER="$(mktemp)"
THEATER_B="$(mktemp)"
trap 'rm -f "$BIN" "$HONEST" "$THEATER" "$THEATER_B"' EXIT
go build -o "$BIN" .

PERIOD=14

echo "=== ЧЕСТНАЯ фикстура (8 деплоев за ${PERIOD} дней) ==="
"$BIN" fixtures/deployments.csv fixtures/incidents.csv "$PERIOD" | tee "$HONEST"

echo
echo "=== ТЕАТР A: дробление надвое, ОБЕ копии аварийного деплоя помечены rework ==="
"$BIN" fixtures/deployments-theater.csv fixtures/incidents-theater.csv "$PERIOD" | tee "$THEATER"

echo
echo "=== ТЕАТР B: то же дробление, но вторая копия классифицирована как плановая ==="
"$BIN" fixtures/deployments-theater-b.csv fixtures/incidents-theater-b.csv "$PERIOD" | tee "$THEATER_B"

metric() { grep -F "$1" "$2" | sed 's/.*: *//' | awk '{print $1}'; }

freq_h=$(metric "Deployment frequency" "$HONEST");   freq_t=$(metric "Deployment frequency" "$THEATER")
lead_h=$(metric "Change lead time" "$HONEST");       lead_t=$(metric "Change lead time" "$THEATER")
recov_h=$(metric "Failed deployment recovery" "$HONEST"); recov_t=$(metric "Failed deployment recovery" "$THEATER")
cfr_h=$(metric "Change fail rate" "$HONEST");        cfr_t=$(metric "Change fail rate" "$THEATER")
rew_h=$(metric "Deployment rework rate" "$HONEST");  rew_t=$(metric "Deployment rework rate" "$THEATER")
freq_b=$(metric "Deployment frequency" "$THEATER_B"); lead_b=$(metric "Change lead time" "$THEATER_B")
recov_b=$(metric "Failed deployment recovery" "$THEATER_B")
cfr_b=$(metric "Change fail rate" "$THEATER_B");     rew_b=$(metric "Deployment rework rate" "$THEATER_B")

echo
echo "=== СРАВНЕНИЕ ==="
echo "метрика                          честно      театр A     театр B"
echo "Deployment frequency             ${freq_h}       ${freq_t}       ${freq_b}"
echo "Change lead time                 ${lead_h}     ${lead_t}     ${lead_b}"
echo "Failed deployment recovery time  ${recov_h}      ${recov_t}      ${recov_b}"
echo "Change fail rate                 ${cfr_h}       ${cfr_t}        ${cfr_b}"
echo "Deployment rework rate           ${rew_h}       ${rew_t}       ${rew_b}"
echo
echo "Разница A и B — только в классификации второй копии аварийного деплоя."
echo "rework rate: ${rew_t} против ${rew_b}. Значит её устойчивость к дроблению —"
echo "следствие разметки данных, а не свойство самой метрики."

echo
echo "ПАДАЮЩИЙ ВАРИАНТ / что проверяем: дробление подняло частоту вдвое и вдвое"
echo "уполовинило change fail rate (инцидент тот же, деплоев больше) — метрики"
echo "«улучшились» без единого изменения в реальной работе. Не сдвинулись в ОБОИХ"
echo "вариантах разметки только change lead time и failed deployment recovery time:"
echo "коммиты и инцидент те же. Именно их несогласованность с частотой и выдаёт"
echo "накрутку. Rework rate на роль такого детектора не годится: её поведение"
echo "зависит от того, как классифицированы новые деплои (A против B выше)."

# Самопроверки: частота накручена, а время доставки и восстановления неподвижны.
awk -v h="$freq_h" -v t="$freq_t" 'BEGIN{ exit !(t > h) }' || {
    echo "ОШИБКА: частота театра не выше честной — нарезки нет." >&2; exit 1; }
if [ "$lead_h" != "$lead_t" ]; then
    echo "ОШИБКА: change lead time театра (${lead_t}) отличается от честного (${lead_h})." >&2
    echo "Фикстура театра обязана сохранять реальные коммиты — разоблачение сломано." >&2
    exit 1
fi
if [ "$recov_h" != "$recov_t" ]; then
    echo "ОШИБКА: failed deployment recovery time разошлось (${recov_h} против ${recov_t})." >&2
    exit 1
fi
# Устойчивость проверяем в ОБОИХ вариантах разметки — только то, что от неё не
# зависит. Прежняя проверка требовала неизменности rework rate и тем самым
# закрепляла артефакт разметки A как якобы свойство метрики.
if [ "$lead_h" != "$lead_b" ] || [ "$recov_h" != "$recov_b" ]; then
    echo "ОШИБКА: в варианте B разошлись lead time (${lead_h}/${lead_b}) или" >&2
    echo "recovery time (${recov_h}/${recov_b}) — фикстуры B построены неверно." >&2
    exit 1
fi
# А вот rework rate между вариантами A и B ОБЯЗАНА различаться: если нет,
# фикстура B не отличается от A и демонстрация зависимости от разметки пропала.
if [ "$rew_t" = "$rew_b" ]; then
    echo "ОШИБКА: rework rate одинакова в вариантах A и B (${rew_t})." >&2
    echo "Разная классификация копий обязана давать разную долю — иначе показать" >&2
    echo "зависимость вывода от разметки нечем." >&2
    exit 1
fi
echo
echo "OK: частота накручена (${freq_h} -> ${freq_t}), lead time (${lead_h}) и recovery (${recov_h})"
echo "устойчивы в обоих вариантах разметки, а rework rate от неё зависит (${rew_t}/${rew_b})."
