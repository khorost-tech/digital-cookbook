# Генератор и dbt-core 1.12 + dbt-postgres 1.11.
FROM python:3.12-slim@sha256:05cda9777409a9c3ffddd94a4c476b79f0769a0b4857f0c7ed9226b6800b0d6f
RUN for i in 1 2 3; do pip install --no-cache-dir dbt-core==1.12.5 dbt-postgres==1.11.0 psycopg2-binary==2.9.11 && break; sleep 5; done \
 && dbt --version
ENV DBT_PROFILES_DIR=/opt/stand/dbt PYTHONPATH=/opt/stand DBT_TARGET_PATH=/tmp/dbt-target DBT_LOG_PATH=/tmp/dbt-logs
WORKDIR /opt/stand
CMD ["sleep", "infinity"]
