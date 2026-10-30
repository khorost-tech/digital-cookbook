"""Единственное место, где собирается `docker run` стенда golangci-lint.

Линтер — образ golangci/golangci-lint по дайджесту из images.json; на хост ничего
не ставится. Каталог стенда монтируется в /src, кеш сборки Go и кеш линтера — в
томе Docker. Раннеры осей зовут функции отсюда, а не строят команды сами.
"""

import json
import os
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CACHE = "golangci-stand-cache"
# Счёт без обрезки: по умолчанию golangci-lint показывает не больше 3 одинаковых
# находок, 50 находок одного линтера и одну находку на строку (ось 3 меряет,
# что это прячет).
NO_LIMITS = ("--max-same-issues", "0", "--max-issues-per-linter", "0", "--uniq-by-line=false")

with open(os.path.join(HERE, "images.json"), encoding="utf-8") as _f:
    IMAGES = json.load(_f)


def pinned(name="golangci-lint"):
    img = IMAGES[name]
    return "%s@%s" % (img["ref"], img["digest"])


def sh(*argv, check=True, timeout=900, cwd=None):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, cwd=cwd)
    out, err = p.stdout.decode("utf-8", "replace"), p.stderr.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("%s: rc=%d %s %s" % (" ".join(argv), p.returncode, out[-1500:], err[-1500:]))
    return p.returncode, out, err


def pull():
    for _ in range(3):
        rc, _, _ = sh("docker", "pull", "-q", pinned(), check=False, timeout=900)
        if rc == 0:
            return
    raise SystemExit("не удалось скачать %s" % pinned())


def run_in(mount, *argv, workdir="/src", binary="golangci-lint", timeout=900):
    """Команда в образе линтера; mount — каталог хоста, который станет /src."""
    return sh("docker", "run", "--rm", "-v", "%s:/src" % mount, "-w", workdir,
              "-v", "%s:/root/.cache" % CACHE, "-v", "%s-mod:/go/pkg/mod" % CACHE,
              pinned(), binary, *argv, check=False, timeout=timeout)


def lint(*args, mount=HERE, workdir="/src", binary="golangci-lint", limits=True):
    """golangci-lint run с JSON-выводом → (находки, код выхода, stderr).

    Код выхода 1 — находки есть; всё, кроме 0 и 1, — ошибка запуска.
    """
    argv = ["run", *args, "--output.json.path", "stdout", "--output.text.path", "stderr"]
    if limits:
        argv += NO_LIMITS
    rc, out, err = run_in(mount, *argv, workdir=workdir, binary=binary)
    if rc not in (0, 1):
        raise SystemExit("golangci-lint rc=%d: %s" % (rc, (err or out)[-2000:]))
    first = out.split("\n", 1)[0].strip()
    report = json.loads(first) if first.startswith("{") else {"Issues": []}
    issues = [{"linter": i["FromLinter"], "file": i["Pos"]["Filename"], "line": i["Pos"]["Line"],
               "text": i["Text"]} for i in (report.get("Issues") or [])]
    return sorted(issues, key=lambda i: (i["file"], i["line"], i["linter"], i["text"])), rc, err


def version():
    _, out, _ = run_in(HERE, "--version")
    return out.strip()
