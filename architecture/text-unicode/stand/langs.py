"""Сборка и запуск одной программы на шести языках, каждая — в своём закреплённом образе.

Программа читает /v/vectors.json и печатает JSON с ответами своего языка. Сетевые шаги (модули Go,
крейты Rust, пакет regex для Python) повторяются: сбой сети при скачивании — не результат опыта.
Кэши — в томах text-unicode-*, уборка стенда их удаляет.
"""

import json
import subprocess
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
IMAGES = json.loads((ROOT / "images.json").read_text())
LANGS = ("go", "rust", "java", "python", "node", "csharp")


def image(name):
    i = IMAGES[name]
    return i["ref"] + "@" + i["digest"]


def sh(argv, check=True, timeout=1800):
    p = subprocess.run(argv, capture_output=True, text=True, timeout=timeout)
    if check and p.returncode:
        raise RuntimeError("command failed: %r\n%s\n%s" % (argv, p.stdout[-2000:], p.stderr[-3000:]))
    return p


def docker(img, workdir, cmd, volumes=(), env=()):
    argv = ["docker", "run", "--rm", "-v", "%s:/v/vectors.json:ro" % (ROOT / "vectors.json"),
            "-v", "%s:/src" % (ROOT / workdir), "-w", "/src"]
    for v in volumes:
        argv += ["-v", v]
    for e in env:
        argv += ["-e", e]
    return argv + [image(img), "sh", "-c", cmd]


def retry(argv, attempts=5):
    for n in range(attempts):
        p = sh(argv, check=False)
        if p.returncode == 0:
            return p
        time.sleep(10)
    raise RuntimeError("не прошло за %d попыток: %r\n%s" % (attempts, argv[-1], p.stderr[-3000:]))


# Подготовка — один раз за прогон: скачать зависимости и собрать; сам запуск — без сети.
PREPARE = {
    "go": ("golang", "go", "go mod download && go build -o /tmp/x ./ && go vet ./...", ["text-unicode-gomod:/go/pkg/mod"], []),
    "rust": ("rust", "rust", "cargo build --release --locked -q", ["text-unicode-cargo:/usr/local/cargo/registry", "text-unicode-target:/src/target"], []),
    "python": ("python", "python", "pip install -q --target /deps -r requirements.txt", ["text-unicode-pydeps:/deps"], []),
    "csharp": ("dotnet", "csharp", "dotnet build -c Release -o /out -nologo -v q", ["text-unicode-dotnet:/out"], []),
}
RUN = {
    "go": ("golang", "go", "go run .", ["text-unicode-gomod:/go/pkg/mod"], ["GOFLAGS=-mod=mod", "GOPROXY=off"]),
    "rust": ("rust", "rust", "/src/target/release/textunicode", ["text-unicode-cargo:/usr/local/cargo/registry", "text-unicode-target:/src/target"], []),
    "java": ("java", "java", "java Main.java", [], []),
    "python": ("python", "python", "python main.py", ["text-unicode-pydeps:/deps"], ["PYTHONPATH=/deps"]),
    "node": ("node", "node", "node main.mjs", [], []),
    "csharp": ("dotnet", "csharp", "dotnet /out/textunicode.dll", ["text-unicode-dotnet:/out"], []),
}


def prepare():
    for lang, (img, wd, cmd, vols, env) in PREPARE.items():
        retry(docker(img, wd, cmd, vols, env))


def run(lang):
    img, wd, cmd, vols, env = RUN[lang]
    out = sh(docker(img, wd, cmd, vols, env), timeout=600).stdout
    return json.loads([l for l in out.splitlines() if l.startswith("{")][-1])


def run_all():
    prepare()
    return {lang: run(lang) for lang in LANGS}


def versions():
    """Версии рантаймов — из самих образов, для README и статьи."""
    cmds = {"golang": "go version", "rust": "rustc --version", "java": "java -version 2>&1 | head -1",
            "python": "python --version", "node": "node --version", "dotnet": "dotnet --version"}
    return {k: sh(["docker", "run", "--rm", image(k), "sh", "-c", c]).stdout.strip() for k, c in cmds.items()}
