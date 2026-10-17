"""Набор на настоящей базе: PostgreSQL 18 в контейнере.

Утверждения — те же самые, из rules.py. Отличается только соединение.
"""

import repo as repo_mod
import rules


def test_unfinished(pg_conn):
    rules.check_unfinished(repo_mod.TaskRepo(pg_conn))


def test_search_is_case_insensitive(pg_conn):
    rules.check_search_is_case_insensitive(repo_mod.TaskRepo(pg_conn))


def test_per_author(pg_conn):
    rules.check_per_author(repo_mod.TaskRepo(pg_conn))
