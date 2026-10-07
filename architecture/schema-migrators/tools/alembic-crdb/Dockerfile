# Обходной путь для CockroachDB: штатный диалект PostgreSQL в SQLAlchemy не
# разбирает строку версии CockroachDB, нужен отдельный диалект cockroachdb.
FROM smig-alembic
RUN pip install --no-cache-dir sqlalchemy-cockroachdb==2.0.4
