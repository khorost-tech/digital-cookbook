"""Запуск bench стенда в контейнере Go по дайджесту.

bench собирается один раз в out/bench (Linux, arm64 или amd64 — по машине) и
запускается с датасетом из стенда vector-databases/dataset, смонтированным
только на чтение. Ядер — все, что дал Docker; поиск меряется в один поток.
"""

import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATASET = os.path.normpath(os.path.join(HERE, "..", "dataset", "data"))
OUT = os.path.join(HERE, "out")

with open(os.path.join(HERE, "images.json"), encoding="utf-8") as f:
    IMAGES = json.load(f)


def image(key):
    i = IMAGES[key]
    return "%s@%s" % (i["ref"], i["digest"])


def sh(*argv, capture=False):
    p = subprocess.run(argv, stdout=subprocess.PIPE if capture else None, stderr=subprocess.PIPE, text=True)
    if p.returncode != 0:
        sys.exit("команда упала (%d): %s\n%s" % (p.returncode, " ".join(argv), p.stderr[-3000:]))
    return p.stdout


def build():
    os.makedirs(OUT, exist_ok=True)
    sh("docker", "run", "--rm", "-v", os.path.join(HERE, "go") + ":/src", "-v", OUT + ":/out",
       "-v", "vdb-gocache:/root/.cache/go-build", "-w", "/src", image("golang"),
       "go", "build", "-o", "/out/bench", "./cmd/bench")


def check_dataset(corpus="quora"):
    for f in ("corpus.f32", "queries.f32", "gt.i32", "gt.rows"):
        if not os.path.exists(os.path.join(DATASET, corpus, f)):
            sys.exit("нет %s/%s/%s — сначала соберите стенд dataset: python3 scripts/make-dataset.py"
                     % (DATASET, corpus, f))


def bench(*args, corpus="quora"):
    """Одна строка JSON от bench → dict."""
    check_dataset(corpus)
    out = sh("docker", "run", "--rm", "-v", DATASET + ":/data:ro", "-v", OUT + ":/out", image("golang"),
             "/out/bench", *args, "-data", "/data/" + corpus, capture=True)
    return json.loads(out.strip().splitlines()[-1])


def command(*args):
    """Команда для статьи — как её повторяет читатель."""
    return "bench " + " ".join(args)


def faiss(*args, corpus="quora"):
    """Сверка с Faiss: контейнер faiss/ (python:3.12-slim по дайджесту + faiss-cpu)."""
    check_dataset(corpus)
    sh("docker", "build", "-q", "-t", "vdb-ann-faiss", os.path.join(HERE, "faiss"))
    out = sh("docker", "run", "--rm", "-v", DATASET + ":/data:ro", "vdb-ann-faiss", "/data/" + corpus, *args,
             capture=True)
    return json.loads(out.strip().splitlines()[-1])


def jvector(*args, corpus="quora"):
    """jvector в контейнере Maven: сборка с повторами (загрузка с Maven Central
    здесь иногда падает с bad_record_mac), затем запуск."""
    check_dataset(corpus)
    java = os.path.join(HERE, "java")
    jar = os.path.join(java, "target", "ann-jvector-1.jar")
    for attempt in range(5):
        p = subprocess.run(["docker", "run", "--rm", "-v", java + ":/p", "-v", "vdb-m2:/root/.m2", "-w", "/p",
                            image("maven"), "mvn", "-q", "-B", "package", "dependency:copy-dependencies",
                            "-DoutputDirectory=target/lib"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                           text=True)
        if p.returncode == 0 and os.path.exists(jar):
            break
    else:
        sys.exit("сборка jvector не удалась:\n" + p.stdout[-3000:])
    out = sh("docker", "run", "--rm", "-v", java + ":/p", "-v", DATASET + ":/data:ro", "-w", "/p", image("maven"),
             "java", "--add-modules", "jdk.incubator.vector", "-cp", "target/ann-jvector-1.jar:target/lib/*",
             "vdb.Bench", "/data/" + corpus, *args, capture=True)
    return json.loads([l for l in out.splitlines() if l.startswith("{")][-1])
