"""Единственное место, где стенд трогает Docker: compose, генератор, dbt, SQL.

Хранилище строится по дням: для каждого дня генератор выставляет состояние
источника app.customers на конец дня, `dbt snapshot` добавляет версии (SCD2),
модель dim_customer_scd3 обновляет текущий и предыдущий регион. Затем — факты и
остальные измерения. Эталон — из генератора (generator/gen.py), не из хранилища.
"""

import json
import os
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DAYS = ["2026-09-%02d" % d for d in range(1, 15)]


def sh(*argv, check=True, timeout=900):
    p = subprocess.run(argv, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("%s: rc=%d\n%s" % (" ".join(argv), p.returncode, out[-3000:]))
    return p.returncode, out


def compose(*args, **kw):
    return sh("docker", "compose", *args, **kw)


def up(fresh=False):
    if fresh:
        compose("down", "-v", check=False)
    for _ in range(3):
        rc, out = compose("up", "-d", "--build", "--wait", check=False, timeout=1800)
        if rc == 0:
            return
    raise SystemExit("стек не поднялся:\n" + out[-2000:])


def tools(*args, check=True):
    return compose("exec", "-T", "tools", *args, check=check)


def dbt(*args, check=True):
    return tools("dbt", *args, "--project-dir", "dbt", check=check)


def sql(query):
    _, out = compose("exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "dwh", "-v", "ON_ERROR_STOP=1",
                     "-AtF", "|", "-c", query)
    return [l.split("|") for l in out.strip().split("\n") if l]


def truth():
    _, out = tools("python", "-m", "generator.gen", "truth")
    return json.loads(out.strip().split("\n")[-1])


def build():
    """Источник заново, хранилище по дням: снапшот и SCD3 каждый день, затем витрины."""
    sql("DROP SCHEMA IF EXISTS staging CASCADE; DROP SCHEMA IF EXISTS marts CASCADE; "
        "DROP SCHEMA IF EXISTS snapshots CASCADE")
    tools("python", "-m", "generator.gen", "reset")
    for d in DAYS:
        tools("python", "-m", "generator.gen", "customers-at", d)
        dbt("snapshot")
        dbt("run", "--select", "+dim_customer_scd3")
    dbt("run")


def rows(query):
    return sql(query)


def kv(query):
    """{ключ: целое} из запроса, отдающего два столбца."""
    return {k: int(float(v)) for k, v in sql(query)}
