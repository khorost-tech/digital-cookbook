# PostgreSQL 18.4 + pg_repack 1.5.3 (внешнее расширение, ставится из PGDG-репозитория,
# уже подключённого в официальном образе). pgstattuple — contrib, идёт в образе.
FROM postgres:18.4

RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-18-repack \
 && rm -rf /var/lib/apt/lists/*
