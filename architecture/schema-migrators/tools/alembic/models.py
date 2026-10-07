# Модель — желаемое состояние после обеих миграций. С ней alembic check
# сравнивает фактическую схему базы: это и есть интроспекция.
from sqlalchemy import Column, Integer, MetaData, Table, Text

metadata = MetaData()

probe = Table(
    "probe",
    metadata,
    Column("id", Integer, primary_key=True, autoincrement=False),
    Column("name", Text),
    Column("note", Text),
)
