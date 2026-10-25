# Airflow 3.3 + dbt-postgres в отдельном окружении: зависимости dbt не должны
# менять зависимости Airflow (и наоборот).
FROM apache/airflow:3.3.2-python3.12@sha256:9df9c8be4096b9cc626bd7cb1f2b8c712eef66c59c615f8c7e6200871ca10bd1
USER root
RUN python3 -m venv /opt/dbt && chown -R airflow: /opt/dbt
USER airflow
RUN for i in 1 2 3; do /opt/dbt/bin/pip install --no-cache-dir dbt-core==1.12.5 dbt-postgres==1.11.0 && break; sleep 5; done \
 && /opt/dbt/bin/dbt --version
RUN for i in 1 2 3; do pip install --no-cache-dir psycopg2-binary==2.9.11 && break; sleep 5; done
ENV DBT_PROFILES_DIR=/opt/stand/dbt PATH="/home/airflow/.local/bin:${PATH}"
