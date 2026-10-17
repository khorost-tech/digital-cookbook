# СТРАТЕГИЯ: то же, что uv, плюс предварительная компиляция байт-кода.
#
# Отличие от плеча uv ровно одно: --compile-bytecode. Разница в размере —
# это вес файлов .pyc, разница на старте — то, ради чего их кладут заранее.
#
# ВАЖНО ПРИ ЧТЕНИИ ТАБЛИЦЫ. pip компилирует байт-код при установке сам, по
# умолчанию. Значит пара «с компиляцией / без» — это uv и uv-compiled, а
# плечо multistage на pip УЖЕ скомпилировано. Прочитать таблицу как «uv
# даёт образ легче pip» и не заметить этого — значит сравнить образ с
# байт-кодом и образ без него.
FROM ghcr.io/astral-sh/uv:0.12.5 AS uvbin

FROM python:3.14 AS build
COPY --from=uvbin /uv /usr/local/bin/uv

COPY index/packages /wheels
COPY images/app/requirements.txt /tmp/requirements.txt

RUN uv venv --python "$(command -v python3.14)" /venv \
    && uv pip install --python /venv/bin/python \
         --no-index --find-links /wheels --compile-bytecode \
         -r /tmp/requirements.txt

FROM python:3.14-slim

COPY --from=build /venv /venv
ENV PATH="/venv/bin:${PATH}"

WORKDIR /srv
COPY images/app/service.py /srv/service.py

EXPOSE 8000
CMD ["python", "-m", "uvicorn", "--host", "0.0.0.0", "--port", "8000", "service:app"]
