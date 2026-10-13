# Стенд волны 5: типизация и данные. Времени этот стенд не меряет.
#
# ПОЧЕМУ ВЕРСИИ ПИНОВАНЫ. Матрица «кто ловит дефект» — утверждение о
# КОНКРЕТНЫХ версиях проверяющих: mypy 2.3 видит не то же, что mypy 1.x, а
# pyright меняет умолчания от релиза к релизу. Плавающая версия превратила
# бы таблицу в снимок случайного дня.
FROM python:3.14-slim

# Node нужен ради pyright: он написан на TypeScript, и питоновская обёртка
# всё равно тянет node при первом запуске — лучше сделать это на сборке,
# чем посреди замера.
RUN apt-get update && apt-get install -y --no-install-recommends \
        curl ca-certificates gnupg \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g pyright@1.1.413

RUN pip install --no-cache-dir --disable-pip-version-check \
        mypy==2.3.1 \
        pydantic==2.13.4 \
        pandas==3.0.5 \
        polars==1.44.0 \
        pyarrow==25.0.1

# Байт-код проверяющих здесь ни на что не влияет, а вот шум в выводе даёт.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app
