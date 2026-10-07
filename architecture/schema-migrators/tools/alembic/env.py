# Обычный env.py, только URL из окружения. Диалект — штатный postgresql:
# стенд проверяет, что видит приложение, подключаясь «как к PostgreSQL».
import os

from alembic import context
from sqlalchemy import create_engine

from models import metadata

engine = create_engine(os.environ["SMIG_URL"])

with engine.connect() as connection:
    context.configure(connection=connection, target_metadata=metadata)
    with context.begin_transaction():
        context.run_migrations()
