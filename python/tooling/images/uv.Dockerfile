# СТРАТЕГИЯ: многоступенчатая, окружение собирает uv.
#
# От предыдущей отличается ТОЛЬКО инструментом установки. Всё остальное —
# базы, что копируется, где лежит окружение — совпадает, иначе разницу
# нельзя было бы приписать инструменту.
FROM ghcr.io/astral-sh/uv:0.12.5 AS uvbin

FROM python:3.14 AS build
COPY --from=uvbin /uv /usr/local/bin/uv

COPY index/packages /wheels
COPY images/app/requirements.txt /tmp/requirements.txt

RUN uv venv --python "$(command -v python3.14)" /venv \
    && uv pip install --python /venv/bin/python \
         --no-index --find-links /wheels -r /tmp/requirements.txt

FROM python:3.14-slim

COPY --from=build /venv /venv
ENV PATH="/venv/bin:${PATH}"

WORKDIR /srv
COPY images/app/service.py /srv/service.py

EXPOSE 8000
CMD ["python", "-m", "uvicorn", "--host", "0.0.0.0", "--port", "8000", "service:app"]
