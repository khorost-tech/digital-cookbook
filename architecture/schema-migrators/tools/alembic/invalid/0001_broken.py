"""broken: невалидный SQL — контрольная проба раннера"""
from alembic import op

revision = "0001"
down_revision = None


def upgrade():
    op.execute("CREATE TABLE broken (id INT PRIMARY KEY,, name TEXT)")


def downgrade():
    op.execute("DROP TABLE broken")
