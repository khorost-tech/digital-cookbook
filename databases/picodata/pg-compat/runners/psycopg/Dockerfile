# Раннер на psycopg живёт в контейнере: так набор проб исполняется одной и той
# же версией драйвера у всех, кто воспроизводит стенд, и не зависит от того,
# что установлено на хосте.
FROM python:3.13-slim
RUN pip install --no-cache-dir "psycopg[binary]==3.2.3"
WORKDIR /app
COPY run.py /app/run.py
ENTRYPOINT ["python", "/app/run.py"]
