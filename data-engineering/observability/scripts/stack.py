"""Единственное место, где стенд трогает Docker: compose, генератор, dbt, Marquez, SQL.

Генератор кладёт загрузки дней в raw и знает инциденты; мониторы
(monitors/monitor.py) снимают метрики и считают тревоги; dbt-ol запускает dbt и
передаёт lineage в Marquez, граф читается через его API.
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
    return out


def monitor(*args):
    _, out = tools("python", "-m", "monitors.monitor", *args)
    return out


def result(out):
    line = [l for l in out.split("\n") if l.startswith("RESULT ")][-1]
    return json.loads(line[len("RESULT "):])


def marquez(path, **params):
    """GET к API Marquez из контейнера tools → JSON."""
    code = ("import json, os, requests; r = requests.get(os.environ['MARQUEZ_URL'] + %r, params=%r, timeout=60); "
            "r.raise_for_status(); print('RESULT ' + json.dumps(r.json()))" % (path, params))
    _, out = tools("python", "-c", code)
    return result(out)


def reset_marquez():
    """Чистая база Marquez: граф не должен зависеть от прошлых прогонов."""
    compose("stop", "marquez")
    sh("docker", "compose", "exec", "-T", "postgres", "psql", "-U", "postgres", "-c",
       "DROP DATABASE IF EXISTS marquez WITH (FORCE)", "-c", "CREATE DATABASE marquez OWNER marquez")
    compose("up", "-d", "--wait", "marquez", timeout=900)


def dbt_ol(*args):
    return compose("exec", "-T", "-w", "/opt/stand/dbt", "tools", "dbt-ol", *args)
