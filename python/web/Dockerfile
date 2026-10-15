FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/astral-sh/uv:0.12.5 /uv /usr/local/bin/uv

ENV UV_PYTHON_INSTALL_DIR=/opt/py-versions
RUN uv python install 3.14
RUN uv venv --python 3.14 /opt/py/web

# Версии пинуются и печатаются из самих пакетов в шапку фикстуры.
# httpx не ставим намеренно: нагрузчик использует свой узкий клиент на
# asyncio (bench/httpfast.py), потому что httpx не насыщал цель — причина
# и числа в его докстринге.
RUN uv pip install --python /opt/py/web/bin/python \
      fastapi==0.141.1 pydantic==2.13.4 \
      django==6.1 \
      flask==3.1.3 \
      uvicorn==0.52.4 gunicorn==26.1.0 uvicorn-worker==0.4.0 \
      pytest==9.1.1

ENV PATH="/opt/py/web/bin:${PATH}"
WORKDIR /app
