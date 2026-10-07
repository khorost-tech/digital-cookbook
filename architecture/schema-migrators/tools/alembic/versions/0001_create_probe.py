"""create probe"""
import sqlalchemy as sa
from alembic import op

revision = "0001"
down_revision = None


def upgrade():
    op.create_table(
        "probe",
        sa.Column("id", sa.Integer, primary_key=True, autoincrement=False),
        sa.Column("name", sa.Text),
    )


def downgrade():
    op.drop_table("probe")
