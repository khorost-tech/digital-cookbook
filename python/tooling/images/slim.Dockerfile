# СТРАТЕГИЯ: то же самое, но на тонкой базе и без следов установки.
#
# Отличий от наивной ровно два: база python:3.14-slim вместо полной и
# колёса не попадают ни в один слой образа. Каталог /wheels подключается
# к одной инструкции RUN через BuildKit (--mount=type=bind) и после неё
# исчезает. `COPY index/packages /wheels` с последующим `rm -rf /wheels`
# так не умеет — даже в том же RUN: COPY уже записал архивы в свой слой,
# и удаление в следующем слое лишь прячет их, образ не худеет ни на байт.
FROM python:3.14-slim

WORKDIR /srv
COPY images/app/requirements.txt /srv/requirements.txt
RUN --mount=type=bind,source=index/packages,target=/wheels \
    pip install --no-cache-dir --no-index --find-links /wheels \
        -r /srv/requirements.txt

COPY images/app/service.py /srv/service.py

EXPOSE 8000
CMD ["python", "-m", "uvicorn", "--host", "0.0.0.0", "--port", "8000", "service:app"]
