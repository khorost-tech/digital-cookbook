# Оптимизированный (--optimized) образ Keycloak для production-стенда (статья 5, HA).
# Двухстадийная сборка: на стадии `build` выполняем `kc.sh build` с зафиксированными
# build-time опциями (БД, кэш, health, metrics). Это «запекает» аугментацию Quarkus
# в образ, после чего контейнер стартует как `start --optimized` — без повторного
# build на каждом запуске (быстрый и предсказуемый старт, production-паттерн).
#
# Пиновка: тот же тег, что и в dev-стенде (docker-compose.yml) — 26.7.0.
ARG KC_VERSION=26.7.0

FROM quay.io/keycloak/keycloak:${KC_VERSION} AS build
# БД — postgres (единственный стейт). Кэш — ispn (distributed Infinispan): в режиме
# `start` он и так по умолчанию, но фиксируем явно на build-стадии. Discovery узлов
# JGroups в Keycloak 26.2+ по умолчанию — JDBC_PING (таблица в той же БД), поэтому
# отдельный cache-stack/JGroups-порты не требуются: обе реплики находят друг друга
# через Postgres. Health и metrics включаем на build-стадии (эндпоинты на :9000).
RUN /opt/keycloak/bin/kc.sh build \
      --db=postgres \
      --cache=ispn \
      --health-enabled=true \
      --metrics-enabled=true

FROM quay.io/keycloak/keycloak:${KC_VERSION}
COPY --from=build /opt/keycloak/ /opt/keycloak/
# Кастомная login-тема и realm-импорт монтируются volume'ами в compose
# (/opt/keycloak/themes, /opt/keycloak/data/import) — в образ не зашиваются,
# чтобы правки realm/темы не требовали пересборки.
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
