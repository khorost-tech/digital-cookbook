"""Общий датасет серии: BEIR Quora и FiQA, эмбеддинги e5, истина точного перебора.

    python3 scripts/make-dataset.py                    → data/<корпус>/…, data/manifest.json
    python3 scripts/make-dataset.py --init-reference   → ещё и reference/<корпус>.sample.f32

Повторный запуск пропускает готовые шаги; FRESH=1 удаляет data/ и строит заново.
Векторы на другом CPU могут отличаться в последних битах, поэтому сборка
сверяется с эталонной выборкой из git не по хешу, а по косинусу.
"""

import hashlib
import json
import os
import shutil
import subprocess
import sys
import urllib.request
import zipfile

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA = os.path.join(HERE, "data")
REF = os.path.join(HERE, "reference")
DIM = 384

with open(os.path.join(HERE, "dataset.json"), encoding="utf-8") as f:
    SPEC = json.load(f)
with open(os.path.join(HERE, "images.json"), encoding="utf-8") as f:
    IMAGES = json.load(f)


def sh(*argv):
    print("$", " ".join(argv), flush=True)
    subprocess.run(argv, check=True)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch(name, spec):
    z = os.path.join(DATA, name + ".zip")
    if not os.path.exists(z):
        urllib.request.urlretrieve(spec["url"], z)
    got = sha256(z)
    if got != spec["sha256"]:
        sys.exit("%s: sha256 %s, ожидался %s" % (z, got, spec["sha256"]))
    if not os.path.exists(os.path.join(DATA, name, "corpus.jsonl")):
        with zipfile.ZipFile(z) as zf:
            zf.extractall(DATA)


def image(key):
    i = IMAGES[key]
    return "%s@%s" % (i["ref"], i["digest"])


def embed(name):
    if os.path.exists(os.path.join(DATA, name, "queries.f32")):
        return
    sh("docker", "build", "-q", "-t", "vdb-embed", os.path.join(HERE, "embed"))
    sh("docker", "run", "--rm", "-v", DATA + ":/data", "vdb-embed", name)


def ground_truth(name, spec):
    if os.path.exists(os.path.join(DATA, name, "gt.i32")):
        return
    gt = spec["ground_truth"]
    sh("docker", "run", "--rm", "-v", os.path.join(HERE, "gt") + ":/src", "-v", DATA + ":/data", "-w", "/src",
       image("golang"), "go", "run", ".", "/data/" + name, str(gt["queries"]), str(gt["k"]))


def rows(path):
    return os.path.getsize(path) // 4 // DIM


def sample(path, n):
    """Каждая шаг-я строка файла векторов: шаг = строк / n."""
    total = rows(path)
    step = max(1, total // n)
    out = bytearray()
    with open(path, "rb") as f:
        for i in range(0, total, step)[:n]:
            f.seek(i * 4 * DIM)
            out += f.read(4 * DIM)
    return bytes(out)


def floats(b):
    import array
    a = array.array("f")
    a.frombytes(b)
    return [a[i:i + DIM] for i in range(0, len(a), DIM)]


def check_reference(name, init):
    got = sample(os.path.join(DATA, name, "corpus.f32"), SPEC["reference"]["sample_rows"])
    ref = os.path.join(REF, name + ".sample.f32")
    if init:
        os.makedirs(REF, exist_ok=True)
        with open(ref, "wb") as f:
            f.write(got)
    with open(ref, "rb") as f:
        want = f.read()
    a, b = floats(got), floats(want)
    if len(a) != len(b):
        sys.exit("%s: в выборке %d строк, в эталоне %d" % (name, len(a), len(b)))
    worst = min(sum(x * y for x, y in zip(u, v)) for u, v in zip(a, b))
    if worst < SPEC["reference"]["min_cosine"]:
        sys.exit("%s: худший косинус с эталоном %.6f < %s" % (name, worst, SPEC["reference"]["min_cosine"]))
    return worst


def main():
    init = "--init-reference" in sys.argv
    if os.environ.get("FRESH") == "1":
        shutil.rmtree(DATA, ignore_errors=True)
    os.makedirs(DATA, exist_ok=True)
    manifest = {}
    for name, spec in SPEC["corpora"].items():
        fetch(name, spec)
        embed(name)
        ground_truth(name, spec)
        d = os.path.join(DATA, name)
        manifest[name] = {"docs": rows(os.path.join(d, "corpus.f32")),
                          "queries": rows(os.path.join(d, "queries.f32")),
                          "gt_queries": sum(1 for _ in open(os.path.join(d, "gt.rows"))),
                          "reference_min_cosine~": round(check_reference(name, init), 6),
                          "sha256~": {f: sha256(os.path.join(d, f)) for f in
                                      ("corpus.f32", "queries.f32", "gt.i32")}}
    with open(os.path.join(DATA, "manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=1)
    print(json.dumps({k: {x: v[x] for x in ("docs", "queries", "gt_queries")} for k, v in manifest.items()}))


if __name__ == "__main__":
    main()
