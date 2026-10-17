# СТРАТЕГИЯ: многоступенчатая сборка, в готовый образ едет только venv.
#
# Ступень сборки может быть какой угодно жирной — в финальный образ из
# неё попадает лишь то, что скопировано явно. Здесь это каталог
# окружения: ни колёс, ни pip-кэша, ни самого pip в готовом образе нет.
FROM python:3.14 AS build

COPY index/packages /wheels
COPY images/app/requirements.txt /tmp/requirements.txt

RUN python -m venv /venv \
    && /venv/bin/pip install --no-cache-dir --no-index --find-links /wheels \
         -r /tmp/requirements.txt

FROM python:3.14-slim

COPY --from=build /venv /venv
ENV PATH="/venv/bin:${PATH}"

WORKDIR /srv
COPY images/app/service.py /srv/service.py

EXPOSE 8000
CMD ["python", "-m", "uvicorn", "--host", "0.0.0.0", "--port", "8000", "service:app"]
