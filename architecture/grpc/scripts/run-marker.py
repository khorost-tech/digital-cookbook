"""Маркер полного прогона → fixtures/RUN-COMPLETE. Ставится ПОСЛЕДНИМ.

В маркере — sha256 каждой фикстуры, число проверок и число диверсий, и всё
это ПОСЧИТАНО, а не вписано: проверки — запуском набора (код 0 обязателен),
диверсии — запуском реестра (код 0 — все сработали). Маркер ставится только
этим скриптом и только после того, как оба прогона отдали код 0.
"""

import hashlib
import json
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FX = os.path.join(HERE, "fixtures")
MARKER = os.path.join(FX, "RUN-COMPLETE")
FIXTURES = ["protocol.jsonl", "deadline.jsonl", "cancel.jsonl", "errors.jsonl", "chain.jsonl", "retry.jsonl",
            "keepalive.jsonl", "balance.jsonl", "size.jsonl"]


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def count_tests():
    # Проверки маркера во время его постановки пропускаются: старый маркер
    # после перезамера заведомо не совпадёт с новыми фикстурами.
    env = dict(os.environ, STAND_WITH_DOCKER="1", STAND_MARKER_PENDING="1")
    p = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "scripts", "-p", "test_*.py"],
                       cwd=HERE, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    out = p.stdout.decode()
    m = re.search(r"^Ran (\d+) tests?", out, re.M)
    if p.returncode != 0 or not m or not re.search(r"^OK", out, re.M):
        raise SystemExit("набор проверок не зелёный — маркер не ставится\n" + out[-2000:])
    return int(m.group(1))


def count_sabotages():
    env = dict(os.environ, STAND_MARKER_PENDING="1")
    p = subprocess.run([sys.executable, "scripts/sabotage.py"], cwd=HERE, env=env,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    out = p.stdout.decode()
    m = re.search(r"диверсий: (\d+), не сработало: (\d+)", out)
    if p.returncode != 0 or not m or m.group(2) != "0":
        raise SystemExit("не все диверсии сработали — маркер не ставится\n" + out[-2000:])
    return int(m.group(1))


def main():
    tests = count_tests()
    sabotages = count_sabotages()
    marker = {"fixtures": {f: sha256(os.path.join(FX, f)) for f in FIXTURES}, "tests": tests,
              "sabotages": sabotages}
    with open(MARKER, "w", encoding="utf-8") as f:
        json.dump(marker, f, ensure_ascii=False, indent=1)
        f.write("\n")
    print(json.dumps({"tests": tests, "sabotages": sabotages}))


if __name__ == "__main__":
    main()
