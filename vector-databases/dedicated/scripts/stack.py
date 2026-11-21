"""Стенд vector-databases/dedicated: движки в compose по одному профилю,
ebench и Java в контейнерах в сети compose.

Профиль поднимается перед осью и гасится после неё вместе с томом: движки
не делят 7,7 ГБ VM Docker друг с другом, а каждая ось начинает с пустого
хранилища.
"""

import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATASET = os.path.normpath(os.path.join(HERE, "..", "dataset", "data"))
OUT = os.path.join(HERE, "out")
PROJECT = "vdb-dedicated"
NETWORK = PROJECT + "_default"
ENGINES = ("qdrant", "milvus", "weaviate")

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
    return ["docker", "compose", "-p", PROJECT, "-f", os.path.join(HERE, "compose.yml"), *args]


def check_dataset():
    for f in ("corpus.f32", "queries.f32", "gt.i32", "gt.f32", "gt.rows"):
        if not os.path.exists(os.path.join(DATASET, "quora", f)):
            sys.exit("нет %s/quora/%s — сначала соберите стенд dataset: python3 scripts/make-dataset.py" % (DATASET, f))


def build():
    os.makedirs(OUT, exist_ok=True)
    sh("docker", "run", "--rm", "-v", os.path.join(HERE, "go") + ":/src", "-v", OUT + ":/out",
       "-v", "vdb-gomod:/go/pkg/mod", "-v", "vdb-gocache:/root/.cache/go-build", "-w", "/src", image("golang"),
       "go", "build", "-o", "/out/ebench", "./cmd/ebench")


def down():
    args = []
    for e in ENGINES:
        args += ["--profile", e]
    subprocess.run(compose(*args, "down", "-v"), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def up(engine):
    """Только этот движок, с пустым томом."""
    check_dataset()
    down()
    for attempt in range(5):  # pull здесь иногда падает с bad record MAC
        p = subprocess.run(compose("--profile", engine, "up", "-d", "--wait"), stdout=subprocess.DEVNULL,
                           stderr=subprocess.PIPE, text=True)
        if p.returncode == 0:
            return
        time.sleep(5 * (attempt + 1))
    sys.exit("compose up %s не удался:\n%s" % (engine, p.stderr[-2000:]))


def ebench(*args):
    """Одна строка JSON от ebench → dict."""
    out = sh("docker", "run", "--rm", "--network", NETWORK, "-v", DATASET + ":/data:ro", "-v", OUT + ":/out",
             image("golang"), "/out/ebench", *args, capture=True)
    return json.loads(out.strip().splitlines()[-1])


def container(engine):
    out = sh("docker", "ps", "--format", "{{.Names}}", capture=True)
    for name in out.split():
        if name.startswith("%s-%s-" % (PROJECT, engine)):
            return name
    sys.exit("нет контейнера %s" % engine)


def memory(engine, settle=30):
    """Память контейнера движка после settle секунд затишья, МиБ: anon — память
    процессов (кучи, векторы в RAM), file — страничный кеш файлов (сегменты и
    индексы на диске, прочитанные или записанные недавно). docker stats
    показывает почти их сумму, и снимок сразу после индексации шумит (фоновые
    оптимизации, сброс кеша)."""
    time.sleep(settle)
    stat = sh("docker", "exec", container(engine), "cat", "/sys/fs/cgroup/memory.stat", capture=True)
    kv = dict(line.split() for line in stat.splitlines() if line.strip())
    return {"anon_mib": round(int(kv["anon"]) / 2 ** 20), "file_mib": round(int(kv["file"]) / 2 ** 20)}


def command(*args):
    return "ebench " + " ".join(args)


def java_dump(engine, ef):
    """Выдача 200 запросов официальным Java-клиентом движка (сборка с повторами)."""
    java = os.path.join(HERE, "java")
    jar = os.path.join(java, "target", "dedicated-java-1.jar")
    for attempt in range(5):
        p = subprocess.run(["docker", "run", "--rm", "-v", java + ":/p", "-v", "vdb-m2:/root/.m2", "-w", "/p",
                            image("maven"), "mvn", "-q", "-B", "package", "dependency:copy-dependencies",
                            "-DoutputDirectory=target/lib"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        if p.returncode == 0 and os.path.exists(jar):
            break
    else:
        sys.exit("сборка Java не удалась:\n" + p.stdout[-3000:])
    out = sh("docker", "run", "--rm", "--network", NETWORK, "-v", java + ":/p", "-v", DATASET + ":/data:ro", "-w", "/p",
             image("maven"), "java", "-cp", "target/dedicated-java-1.jar:target/lib/*", "vdb.Dump", engine,
             "/data/quora", str(ef), capture=True)
    return json.loads([l for l in out.splitlines() if l.startswith("{")][-1])
