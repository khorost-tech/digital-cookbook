FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl procps git \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/astral-sh/uv:0.12.5 /uv /usr/local/bin/uv

ENV UV_PYTHON_INSTALL_DIR=/opt/py-versions
RUN uv python install 3.14

# Отдельное окружение под сами инструменты. Держать Poetry и pip в том же
# окружении, куда они ставят зависимости подопытного проекта, нельзя:
# установка затирала бы инструмент, которым её делают, и «одинаковый набор
# пакетов» переставал бы быть одинаковым.
RUN uv venv --python 3.14 /opt/py/tools
RUN uv pip install --python /opt/py/tools/bin/python \
      poetry==2.3.1 \
      pip==26.0

ENV PATH="/opt/py/tools/bin:${PATH}"

# Poetry обязан ставить в окружение, которое создаём мы, а не заводить своё
# по своему усмотрению: иначе у него появляется работа, которой нет у
# остальных, и сравнение перестаёт быть сравнением.
ENV POETRY_VIRTUALENVS_CREATE=false \
    POETRY_NO_INTERACTION=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app
