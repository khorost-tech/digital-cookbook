"""Единственное место, где стенд трогает Docker: compose, задания tools и Spark.

Задания печатают итог строкой «RESULT {json}»; раннеры собирают из неё строку
фикстуры. Каждое задание само создаёт свою таблицу или каталог «папки» заново.
"""

import json
import os
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def sh(*argv, check=True, timeout=1800):
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
        rc, out = compose("up", "-d", "--build", "--wait", check=False, timeout=3600)
        if rc == 0:
            break
    else:
        raise SystemExit("стек не поднялся:\n" + out[-2000:])
    tools_py("import boto3, os\n"
             "s3 = boto3.client('s3', endpoint_url=os.environ['STAND_S3'])\n"
             "try:\n    s3.create_bucket(Bucket='warehouse')\nexcept Exception:\n    pass\n"
             "from jobs import lake\nlake.catalog().create_namespace_if_not_exists('shop')")


def result(out, what):
    lines = [l for l in out.split("\n") if l.startswith("RESULT ")]
    if not lines:
        raise SystemExit("нет итога от %s:\n%s" % (what, out[-3000:]))
    return json.loads(lines[-1][len("RESULT "):])


def tools(module, *args):
    _, out = compose("exec", "-T", "tools", "python", "-m", module, *args)
    return result(out, module)


def tools_py(code):
    return compose("exec", "-T", "tools", "python", "-c", code)


def spark(job, *args):
    _, out = compose("exec", "-T", "spark", "/opt/spark/bin/spark-submit", "jobs/%s.py" % job, *args)
    return result(out, job)


def objects(prefix):
    _, out = tools_py("from jobs import lake; print('N', lake.objects(%r))" % prefix)
    return int([l for l in out.split("\n") if l.startswith("N ")][-1][2:])
