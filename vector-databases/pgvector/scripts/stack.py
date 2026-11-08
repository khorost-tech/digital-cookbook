"""Стенд vector-databases/pgvector: PostgreSQL в compose, vbench и Java в контейнерах.

vbench собирается в out/vbench и запускается в контейнере Go в сети compose, с
датасетом из стенда vector-databases/dataset (только чтение). Таблица docs
загружается один раз; `FRESH=1 bash scripts/run-all.sh` начинает с пустого тома.
"""

import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATASET = os.path.normpath(os.path.join(HERE, "..", "dataset", "data"))
OUT = os.path.join(HERE, "out")
PROJECT = "vdb-pgvector"
NETWORK = PROJECT + "_default"

with open(os.path.join(HERE, "images.json"), encoding="utf-8") as f:
    IMAGES = json.load(f)


def image(key):
    i = IMAGES[key]
    return "%s@%s" % (i["ref"], i["digest"])


def sh(*argv, capture=False):
    p = subprocess.run(argv, stdout=subprocess.PIPE if capture else subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if p.returncode != 0:
        sys.exit("команда упала (%d): %s\n%s" % (p.returncode, " ".join(argv), p.stderr[-3000:]))
    return p.stdout


def compose(*args):
    return sh("docker", "compose", "-p", PROJECT, "-f", os.path.join(HERE, "compose.yml"), *args, capture=True)


def check_dataset():
    for f in ("corpus.f32", "queries.f32", "gt.i32", "gt.f32", "gt.rows"):
        if not os.path.exists(os.path.join(DATASET, "quora", f)):
            sys.exit("нет %s/quora/%s — сначала соберите стенд dataset: python3 scripts/make-dataset.py" % (DATASET, f))


def build():
    os.makedirs(OUT, exist_ok=True)
    sh("docker", "run", "--rm", "-v", os.path.join(HERE, "go") + ":/src", "-v", OUT + ":/out",
       "-v", "vdb-gomod:/go/pkg/mod", "-v", "vdb-gocache:/root/.cache/go-build", "-w", "/src", image("golang"),
       "go", "build", "-o", "/out/vbench", "./cmd/vbench")


def up():
    """PostgreSQL поднят, vbench собран, таблица docs загружена."""
    check_dataset()
    for attempt in range(5):  # pull здесь иногда падает с bad record MAC
        p = subprocess.run(["docker", "compose", "-p", PROJECT, "-f", os.path.join(HERE, "compose.yml"),
                            "up", "-d", "--wait"], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        if p.returncode == 0:
            break
        time.sleep(5 * (attempt + 1))
    else:
        sys.exit("compose up не удался:\n" + p.stderr[-2000:])
    build()
    return vbench("load")


def vbench(*args):
    """Одна строка JSON от vbench → dict."""
    out = sh("docker", "run", "--rm", "--network", NETWORK, "-v", DATASET + ":/data:ro", "-v", OUT + ":/out",
             image("golang"), "/out/vbench", *args, capture=True)
    return json.loads(out.strip().splitlines()[-1])


def java_dump(ef):
    """Выдача 200 запросов через JDBC + pgvector-java (сборка с повторами)."""
    java = os.path.join(HERE, "java")
    jar = os.path.join(java, "target", "pgvector-jdbc-1.jar")
    for attempt in range(5):
        p = subprocess.run(["docker", "run", "--rm", "-v", java + ":/p", "-v", "vdb-m2:/root/.m2", "-w", "/p",
                            image("maven"), "mvn", "-q", "-B", "package", "dependency:copy-dependencies",
                            "-DoutputDirectory=target/lib"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        if p.returncode == 0 and os.path.exists(jar):
            break
    else:
        sys.exit("сборка Java не удалась:\n" + p.stdout[-3000:])
    out = sh("docker", "run", "--rm", "--network", NETWORK, "-v", java + ":/p", "-v", DATASET + ":/data:ro", "-w", "/p",
             image("maven"), "java", "-cp", "target/pgvector-jdbc-1.jar:target/lib/*", "vdb.Dump", "/data/quora",
             "jdbc:postgresql://pg:5432/vdb", str(ef), capture=True)
    return json.loads([l for l in out.splitlines() if l.startswith("{")][-1])


def command(*args):
    return "vbench " + " ".join(args)
