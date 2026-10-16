FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl procps postgresql-client \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/astral-sh/uv:0.12.5 /uv /usr/local/bin/uv

ENV UV_PYTHON_INSTALL_DIR=/opt/py-versions
RUN uv python install 3.14
RUN uv venv --python 3.14 /opt/py/data

# Версии пинуются и печатаются из самих пакетов в шапку фикстуры. Пины
# проставлены после первой сборки без них: выдумывать номера версий нельзя,
# их надо взять у резолвера.
RUN uv pip install --python /opt/py/data/bin/python \
      sqlalchemy==2.0.52 \
      asyncpg==0.31.0 \
      psycopg[binary]==3.3.4 \
      alembic==1.19.1 \
      pytest==9.1.1

ENV PATH="/opt/py/data/bin:${PATH}"
WORKDIR /app
