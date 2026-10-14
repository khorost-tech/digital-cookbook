# Два интерпретатора в одном образе: обычная сборка CPython и free-threaded
# (PEP 703). Официальных docker-тегов вида 3.14t не существует, поэтому обе
# сборки ставит uv — он же тема шестой статьи серии.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl procps \
    && rm -rf /var/lib/apt/lists/*

# Версия uv пинуется: плавающий latest сделает стенд невоспроизводимым.
COPY --from=ghcr.io/astral-sh/uv:0.12.5 /uv /usr/local/bin/uv

ENV UV_PYTHON_INSTALL_DIR=/opt/py-versions

# Суффикс t запрашивает free-threaded вариант той же версии. Без него uv
# выбрал бы обычную сборку: на 3.14 GIL-вариант остаётся предпочтительным.
RUN uv python install 3.14 3.14t

# Отдельный venv на каждой сборке: плечи не делят site-packages и не могут
# незаметно повлиять друг на друга установленными пакетами.
RUN uv venv --python 3.14  /opt/py/gil \
    && uv venv --python 3.14t /opt/py/nogil

# pytest в обе сборки: тесты измерителя обязаны проходить на каждой из них.
# Версия пинуется по той же причине, что и версия uv.
RUN uv pip install --python /opt/py/gil/bin/python   pytest==9.1.1
RUN uv pip install --python /opt/py/nogil/bin/python pytest==9.1.1

# py-spy — сэмплирующий профайлер, снимает стек живого процесса извне.
# Ставится только в сборку с GIL: это инструмент, а не библиотека плеча.
RUN uv pip install --python /opt/py/gil/bin/python py-spy==0.4.2

WORKDIR /bench
