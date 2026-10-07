"""add note"""
import sqlalchemy as sa
from alembic import op

revision = "0002"
down_revision = "0001"


def upgrade():
    op.add_column("probe", sa.Column("note", sa.Text))


def downgrade():
    op.drop_column("probe", "note")
