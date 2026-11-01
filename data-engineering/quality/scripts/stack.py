"""Единственное место, где стенд трогает Docker: compose, генератор, dbt, GE, SQL.

Генератор кладёт партию с дефектом в raw и знает плохие строки; проверки — тесты
dbt и source freshness (результаты из run_results.json/sources.json) и набор
Great Expectations (checks/ge_suite.py).
"""

import json
import os
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


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


def gen(*args):
    _, out = tools("python", "-m", "generator.gen", *args)
    return json.loads(out.strip().split("\n")[-1])


def dbt_results(kind="run_results"):
    """run_results.json / sources.json последнего вызова dbt в контейнере."""
    _, out = tools("cat", "/tmp/dbt-target/%s.json" % kind)
    return json.loads(out)


def ge():
    _, out = tools("python", "-m", "checks.ge_suite")
    line = [l for l in out.split("\n") if l.startswith("RESULT ")][-1]
    return json.loads(line[len("RESULT "):])
