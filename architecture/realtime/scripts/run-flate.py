"""Проба к оси 1: compress/flate Go при сбросе после каждого сообщения → fixtures/flate.jsonl.

Объясняет расхождение Go и Java в сжатых вариантах оси 1 замером, а не
догадкой: та же библиотека, то же сообщение, три уровня сжатия.

    python3 scripts/run-flate.py
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "flate.jsonl")
CMD = "python3 scripts/run-flate.py"


def main():
    out = docker.sh("docker", "run", "--rm", "-v", "%s:/src:ro" % os.path.join(docker.HERE, "probes", "flate"),
                    "-w", "/src", "-e", "GOFLAGS=-buildvcs=false", docker.pinned("golang"), "go", "run", ".")
    rows = []
    for line in out.splitlines():
        if line.startswith("{"):
            row = json.loads(line)
            row["cmd"] = CMD
            rows.append(row)
            print(json.dumps(row, ensure_ascii=False))
    if len(rows) != 3:
        raise SystemExit("проба flate: ожидалось 3 строки, получено %d\n%s" % (len(rows), out))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(docker_end() + "\n")


def docker_end():
    from scripts.outcome import END_MARKER
    return END_MARKER


if __name__ == "__main__":
    main()
