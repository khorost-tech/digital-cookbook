#!/usr/bin/env bash
# Поднимает ОДИН конвейер.
# Использование: ./scripts/up.sh <профиль> [вариант]
#   ./scripts/up.sh vector | otelcol | fluentbit
#   ./scripts/up.sh combo [json|otlp]     — вариант связки, по умолчанию otlp
#   ./scripts/up.sh otlp  [decoded|raw]   — use_otlp_decoding у Vector
#
# Сервисы перечисляются ЯВНО, а не поднимается профиль целиком: генератор сидит
# в профиле gen и запускаться сам не должен. На соседнем стенде неявный запуск
# генератора дал 126355 событий вместо 2067 и обесценил замер.
set -euo pipefail

PROFILE="${1:-vector}"
cd "$(dirname "$0")/.."

# Страховка для Windows/Git Bash: пути вида /etc/vector/... в переменных
# окружения MSYS переписывает в Windows-пути при вызове docker.exe.
export MSYS_NO_PATHCONV=1

# Второй аргумент — вариант профиля. Нужен там, где у профиля не один конфиг:
#   combo json  — наивная сборка, codec: json. Поток теряется целиком.
#   combo otlp  — рабочая, с ручной укладкой события в модель OTel (умолчание).
#   otlp decoded|raw — приём OTLP Vector'ом с use_otlp_decoding true|false.
VARIANT="${2:-}"

case "$PROFILE" in
  vector)     SERVICES=(opensearch prometheus vector) ;;
  otelcol)    SERVICES=(opensearch prometheus otelcol) ;;
  fluentbit)  SERVICES=(opensearch prometheus fluentbit) ;;
  combo)      SERVICES=(opensearch prometheus vector otelcol) ;;
  otlp)       SERVICES=(opensearch prometheus otelcol vector) ;;
  *) echo "профиль: vector | otelcol | fluentbit | combo | otlp" >&2; exit 2 ;;
esac

# Конфиги профилей, у которых умолчание из docker-compose.yml не подходит.
# Без этого `up.sh combo` поднимал бы обычные agent.yaml и config.yaml, то есть
# два независимых конвейера вместо связки, и стенд не воспроизводил бы то, что
# описано в README. Переменные, заданные снаружи, имеют приоритет — на них
# опираются скрипты замеров.
case "$PROFILE" in
  combo)
    case "${VARIANT:-otlp}" in
      json) : "${VECTOR_CONFIG:=/etc/vector/combo-agent.yaml}" ;;
      otlp) : "${VECTOR_CONFIG:=/etc/vector/combo-agent-otlp.yaml}" ;;
      *) echo "вариант combo: json | otlp" >&2; exit 2 ;;
    esac
    : "${OTELCOL_CONFIG:=/etc/otelcol/combo-hub.yaml}"
    export VECTOR_CONFIG OTELCOL_CONFIG
    ;;
  otlp)
    case "${VARIANT:-decoded}" in
      decoded) : "${VECTOR_CONFIG:=/etc/vector/otlp-in.yaml}" ;;
      raw)     : "${VECTOR_CONFIG:=/etc/vector/otlp-in-nodecode.yaml}" ;;
      *) echo "вариант otlp: decoded | raw" >&2; exit 2 ;;
    esac
    : "${OTELCOL_CONFIG:=/etc/otelcol/otlp-in.yaml}"
    export VECTOR_CONFIG OTELCOL_CONFIG
    ;;
esac

# Гасим конвейеры, которые в этот профиль не входят. Без этого недавно
# поднятый соседний конвейер продолжает читать те же файлы и писать в свой
# индекс — то есть замер CPU идёт при конкуренции за ядра, а «поднимается
# всегда ровно один» превращается в благое пожелание. Живьём: bench.sh для
# vector стартовал при работающем fluentbit.
ALL_PIPELINES=(vector otelcol fluentbit)
for p in "${ALL_PIPELINES[@]}"; do
  keep=0
  for s in "${SERVICES[@]}"; do [[ "$s" == "$p" ]] && keep=1; done
  (( keep )) || docker compose stop "$p" >/dev/null 2>&1 || true
done

# Том состояния Collector надо отдать его пользователю ДО старта.
# Образ otel/opentelemetry-collector-contrib запускается от uid 10001 (проверено
# `docker image inspect -f '{{.Config.User}}'`), а docker создаёт named volume
# как root:root — file_storage падает с "permission denied" ещё на старте
# ресиверов. Vector и Fluent Bit этой проблемы не знают просто потому, что их
# образы идут от root. Понижать Collector до root ради удобства стенда
# неправильно: непривилегированный по умолчанию — его достоинство, а не помеха.
# В k8s ровно это делает initContainer.
for svc in "${SERVICES[@]}"; do
  if [[ "$svc" == "otelcol" ]]; then
    docker volume create vector-vs-collector_otelcol-data >/dev/null
    docker run --rm -v vector-vs-collector_otelcol-data:/data busybox:1.37 \
      chown -R 10001:10001 /data
  fi
done

docker compose --profile "$PROFILE" up -d "${SERVICES[@]}"
echo "поднят профиль $PROFILE${VARIANT:+ (вариант $VARIANT)}: ${SERVICES[*]}"
