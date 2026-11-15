"""Store plugin tasks apart from the worker job queue.

The Python worker claims pending rows in jobs. A plugin import stays here so
that claim never sees an unknown kind, and the import URL is not a worker payload.
"""
from alembic import op
from sqlalchemy import CheckConstraint, Column, ForeignKey, Integer, Text, inspect, text

from musik.db.migrations import migrate_connection


revision = "musik_15"
down_revision = "musik_14"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    if connection.dialect.name == "sqlite":
        migrate_connection(connection.connection.driver_connection)
        return

    tables = set(inspect(connection).get_table_names())
    if "plugin_jobs" in tables:
        return
    op.create_table(
        "plugin_jobs",
        Column("id", Integer, primary_key=True, autoincrement=True),
        Column("plugin_id", Text, nullable=False),
        Column("action", Text, nullable=False),
        Column("input_json", Text, nullable=False),
        Column("status", Text, nullable=False, server_default=text("'pending'")),
        Column("progress_json", Text),
        Column("result_json", Text),
        Column("error", Text),
        Column("error_code", Text),
        Column("downstream_job_id", Integer, ForeignKey("jobs.id")),
        Column("created_at", Text, nullable=False),
        Column("updated_at", Text, nullable=False),
        CheckConstraint(
            "status IN ('pending', 'running', 'done', 'error', 'cancelled')",
            name="ck_plugin_jobs_status",
        ),
    )
    op.create_index("idx_plugin_jobs_status", "plugin_jobs", ["status", "id"])


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
