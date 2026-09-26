# PgBouncer 1.25.2 из PGDG-репозитория (уже подключён в официальном образе postgres).
# Свой образ, а не edoburu — нужен полный контроль над pgbouncer.ini (меняем pool_mode).
# Конфиг монтируется томом (см. docker-compose), а не копируется, — удобнее для правок.
FROM postgres:18.4

RUN apt-get update \
 && apt-get install -y --no-install-recommends pgbouncer \
 && rm -rf /var/lib/apt/lists/*

USER postgres
EXPOSE 6432
CMD ["pgbouncer", "/etc/pgbouncer/pgbouncer.ini"]
