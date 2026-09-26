# PostgreSQL 18.4 + два инструмента PITR: pgBackRest (из PGDG apt) и wal-g (бинарник
# из релиза). Оба демонстрируются на отдельных инстансах (у каждого свой archive_command).
# wal-g качается через HTTP-прокси при сборке: docker build нужен --build-arg HTTPS_PROXY=...
FROM postgres:18.4

ARG HTTPS_PROXY=""
ARG WALG_VERSION=v3.0.8

RUN apt-get update \
 && apt-get install -y --no-install-recommends pgbackrest curl ca-certificates \
 && https_proxy="$HTTPS_PROXY" curl -fsSL \
      "https://github.com/wal-g/wal-g/releases/download/${WALG_VERSION}/wal-g-pg-24.04-amd64.tar.gz" \
      -o /tmp/wg.tar.gz \
 && tar -xzf /tmp/wg.tar.gz -C /tmp \
 && mv /tmp/wal-g-pg-24.04-amd64 /usr/local/bin/wal-g \
 && chmod +x /usr/local/bin/wal-g \
 && rm -rf /tmp/wg.tar.gz /var/lib/apt/lists/* \
 && pgbackrest version && wal-g --version
