"""Набор на фейке: настоящий SQL, но другая СУБД."""

import repo as repo_mod
import rules


def test_unfinished(sqlite_conn):
    rules.check_unfinished(repo_mod.TaskRepo(sqlite_conn))


def test_search_is_case_insensitive(sqlite_conn):
    rules.check_search_is_case_insensitive(repo_mod.TaskRepo(sqlite_conn))


def test_per_author(sqlite_conn):
    rules.check_per_author(repo_mod.TaskRepo(sqlite_conn))
