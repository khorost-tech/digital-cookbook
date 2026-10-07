# Alembic со своими зависимостями, версии закреплены.
FROM python:3.13-slim
RUN pip install --no-cache-dir alembic==1.20.0 SQLAlchemy==2.1.3 'psycopg[binary]==3.3.6'
WORKDIR /w
ENTRYPOINT ["alembic"]
