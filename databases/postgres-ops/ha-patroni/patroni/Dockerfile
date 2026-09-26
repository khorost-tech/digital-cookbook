# Patroni поверх PostgreSQL 18.4. Patroni сам управляет postgres (initdb, promote,
# reload), а координируется через etcd. pgBackRest — для PITR (сц.04). wal-g добавим
# отдельным бинарником там же.
FROM postgres:18.4

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      python3-pip python3-psycopg2 pgbackrest curl ca-certificates \
 && pip3 install --break-system-packages --no-cache-dir "patroni[etcd3]" \
 && rm -rf /var/lib/apt/lists/*

# Patroni версия — зафиксировать по факту сборки (см. README)
RUN patroni --version

EXPOSE 5432 8008
USER postgres
