#!/usr/bin/env bash
#
# probe.sh — фактчек-гейт. Печатает ФАКТИЧЕСКИЕ версии и подтверждает
# свойства, на которые статьи будут ссылаться. Всё, что не подтвердилось
# здесь, в статью попасть не должно.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/probe.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

echo "=== Версии ==="

# Снимаем версии в переменные ОТДЕЛЬНЫМ шагом, а не внутри аргумента printf:
# под `set -e` падение подстановки команд, использованной как аргумент
# другой команды, скрипт не останавливает — printf всё равно вернёт 0, и
# гейт напечатает пустую строку вместо версии, но дойдёт до "ГЕЙТ ПРОЙДЕН".
# Каждое значение дополнительно проверяется на пустоту: пустая версия в
# фикстурах — это подделка факта, а не мелочь.

pg_version="$(sql_value "SHOW server_version;")" \
    || fail "Postgres: SHOW server_version не выполнился"
[ -n "$pg_version" ] || fail "Postgres: версия пустая"
printf '  Postgres:  %s\n' "$pg_version"

kurrentdb_info="$(curl -sf --max-time 5 "http://127.0.0.1:${ESDB_PORT}/info")" \
    || fail "KurrentDB: GET /info недоступен (curl упал) — сервис не отвечает на 127.0.0.1:${ESDB_PORT}"
kurrentdb_version="$(printf '%s' "$kurrentdb_info" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("esVersion",""))')" \
    || fail "KurrentDB: /info вернул не-JSON, версию не извлечь"
[ -n "$kurrentdb_version" ] || fail "KurrentDB: поле esVersion пустое или отсутствует в ответе /info"
printf '  KurrentDB: %s\n' "$kurrentdb_version"

# kafka-broker-api-versions.sh печатает id брокера и таблицу протокольных
# версий API, а не версию Kafka — сама версия там не присутствует. Реальную
# версию отдаёт только kafka-topics.sh --version.
kafka_version="$(docker exec esd-kafka /opt/kafka/bin/kafka-topics.sh --version 2>/dev/null)" \
    || fail "Kafka: kafka-topics.sh --version упал — брокер недоступен изнутри контейнера"
[ -n "$kafka_version" ] || fail "Kafka: версия пустая"
printf '  Kafka:     %s\n' "$kafka_version"

echo
echo "=== Оптимистичная блокировка на уровне стрима ==="
# Postgres: PRIMARY KEY (stream_id, version) даёт её бесплатно.
# data->>'type' обязан совпадать с event_type — ограничение
# events_type_matches_payload, см. sql/01-schema.sql. Поэтому здесь не
# пустой JSON, как было раньше, а согласованный.
psql_c -c "INSERT INTO events (stream_id, version, event_type, data)
           VALUES ('probe', 1, 'X', '{\"type\":\"X\"}'::jsonb);" >/dev/null
if psql_c -c "INSERT INTO events (stream_id, version, event_type, data)
              VALUES ('probe', 1, 'X', '{\"type\":\"X\"}'::jsonb);" >/dev/null 2>&1; then
    fail "Postgres принял повторную версию — оптимистичной блокировки НЕТ, вся серия на этом стоит"
fi
log "Postgres: повторная версия отвергнута (SQLSTATE 23505) — блокировка работает"

# И сразу — ограничение согласованности типа: противоречивая строка не
# должна записываться вовсе.
if psql_c -c "INSERT INTO events (stream_id, version, event_type, data)
              VALUES ('probe', 2, 'OxygenConsumed', '{\"type\":\"OxygenAdded\",\"liters\":7}'::jsonb);" \
              >/dev/null 2>&1; then
    fail "Postgres принял строку, где event_type и data->>'type' противоречат друг другу — ограничение events_type_matches_payload не работает"
fi
log "Postgres: противоречие event_type и data->>'type' отвергнуто ограничением"
psql_c -c "DELETE FROM events WHERE stream_id = 'probe';" >/dev/null

echo
echo "=== Kafka: чего у неё нет ==="
log "проверяем ФАКТОМ, а не утверждением: у Kafka нет условной записи"
log "по версии ключа — producer API её просто не предоставляет."
docker exec esd-kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
    --create --if-not-exists --topic probe-events --partitions 3 --replication-factor 1 >/dev/null
log "топик probe-events создан: 3 партиции — то есть порядок гарантирован только внутри партиции"

echo
echo "=== Kafka: внешний путь с хоста ==="
# Всё выше про Kafka ходило через `docker exec ... --bootstrap-server
# localhost:9092` — это внутренний листенер ИЗНУТРИ контейнера. Проброс
# ${KAFKA_PORT}:9094 (EXTERNAL://127.0.0.1:${KAFKA_PORT}) при этом не
# проверяется вообще. Именно этим путём — 127.0.0.1:${KAFKA_PORT} с хоста —
# в задаче 5 пойдёт нагрузочный генератор на Go. Проверяем ровно его:
# одноразовый контейнер в сетевом namespace хоста (--network host)
# обращается к брокеру так же, как обращался бы процесс на хосте.
#
# Образ берём ИЗ compose/compose.yml, а не хардкодим тегом отдельно: гейт
# verify-static.sh пинует версию образа только там, дублирующая строка тега
# здесь могла бы разойтись с compose при бампе версии и пройти незамеченной
# (probe.sh проверял бы уже не тот образ, что реально поднят стендом).
kafka_probe_image="$(docker compose -f "$COMPOSE" config --images kafka)" \
    || fail "Kafka: не удалось узнать образ сервиса kafka из $COMPOSE"
[ -n "$kafka_probe_image" ] || fail "Kafka: docker compose config --images kafka вернул пустую строку"
docker run --rm --network host "$kafka_probe_image" \
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server "127.0.0.1:${KAFKA_PORT}" --list >/dev/null \
    || fail "Kafka: 127.0.0.1:${KAFKA_PORT} недоступен С ХОСТА — внешний листенер EXTERNAL сломан, хотя внутренний рабочий; задача 5 упадёт на этом же месте"
log "Kafka отвечает на 127.0.0.1:${KAFKA_PORT} с хоста ($kafka_probe_image) — внешний листенер EXTERNAL исправен"

echo
log "ГЕЙТ ПРОЙДЕН — версии зафиксированы, свойства подтверждены"
