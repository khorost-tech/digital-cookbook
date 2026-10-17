# СТРАТЕГИЯ: distroless — в образе нет дистрибутива.
#
# Ни оболочки, ни менеджера пакетов, ни утилит: только glibc, набор
# системных библиотек и то, что положили руками. Смысл не в размере — по
# весу distroless проигрывает хорошо собранной slim-многоступенчатой
# сборке, — а в поверхности атаки: чего в образе нет, того в нём и не
# запустить.
#
# ПОЧЕМУ ИНТЕРПРЕТАТОР ЕДЕТ ОТДЕЛЬНО. Официальный distroless/python3
# приносит свою версию Python, и она не 3.14 — окружение, собранное под
# 3.14, на ней просто не заведётся. Поэтому интерпретатор берётся
# автономной сборкой (uv python install): она перемещаемая и тянет с
# собой свои библиотеки, а не рассчитывает найти их в системе.
#
# ПУТИ ОБЯЗАНЫ СОВПАДАТЬ. Окружение помнит, каким интерпретатором
# создано, по абсолютному пути. Скопировать /opt/py в другое место —
# получить образ, который собирается без единой жалобы и падает при
# первом запуске.
FROM ghcr.io/astral-sh/uv:0.12.5 AS uvbin

FROM debian:bookworm-slim AS build
COPY --from=uvbin /uv /usr/local/bin/uv

ENV UV_PYTHON_INSTALL_DIR=/opt/py
RUN uv python install 3.14

COPY index/packages /wheels
COPY images/app/requirements.txt /tmp/requirements.txt

RUN uv venv --python 3.14 /venv \
    && uv pip install --python /venv/bin/python \
         --no-index --find-links /wheels -r /tmp/requirements.txt

COPY images/app/service.py /srv/service.py

FROM gcr.io/distroless/base-debian12

# libgcc НЕ ВХОДИТ в distroless/base, и это первое, обо что стенд
# споткнулся. Собиралось всё безупречно; контейнер падал при запуске на
# импорте pydantic_core: «libgcc_s.so.1: cannot open shared object file».
# Колёса с нативным кодом (pydantic-core, orjson — оба на Rust) рассчитывают
# найти её в системе, а в distroless системы нет.
#
# Отсюда общее правило этого плеча: distroless не «то же самое, только
# меньше». Всё, что зависимости ожидают от дистрибутива, придётся принести
# руками, и узнаётся этот список не на сборке, а на первом запуске.
COPY --from=build /lib/x86_64-linux-gnu/libgcc_s.so.1 /lib/x86_64-linux-gnu/
COPY --from=build /opt/py /opt/py
COPY --from=build /venv /venv
COPY --from=build /srv /srv

WORKDIR /srv
EXPOSE 8000
# ENTRYPOINT списком, а не строкой: оболочки, которая разобрала бы
# строку, в образе нет.
ENTRYPOINT ["/venv/bin/python", "-m", "uvicorn", \
            "--host", "0.0.0.0", "--port", "8000", "service:app"]
