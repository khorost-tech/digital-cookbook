"""Версии инструментов замером → fixtures/tools.jsonl.

Три поля у каждого инструмента: `declared` — что обещает пакетный менеджер
(`apt-cache policy`, строка Installed), `measured` — что сказал сам инструмент,
`raw` — его сырой вывод целиком. Версия, которой нет в `raw`, взята не у
инструмента, а из объявления (урок волны A: `10.1.1+ds1-4` объявлено, `10.1.0`
измерено). У GoogleTest и Google Benchmark своего `--version` нет: их
«собственный вывод» — запись dpkg о собранном пакете, и это названо в строке.
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, read_measurements  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "tools.jsonl")

# инструмент: (пакет для apt-cache, команда «собственного» вывода, regex версии)
TOOLS = {
    "gcc": ("g++", "g++ --version", r"\) (\d+\.\d+\.\d+)"),
    "clang": ("clang", "clang++ --version", r"clang version (\d+\.\d+\.\d+)"),
    "valgrind": ("valgrind", "valgrind --version", r"valgrind-(\d+\.\d+\.\d+)"),
    "cmake": ("cmake", "cmake --version", r"cmake version (\d+\.\d+\.\d+)"),
    "gtest": ("libgtest-dev", "dpkg-query -W -f='${Package} ${Version}\\n' libgtest-dev",
              r"libgtest-dev (\S+)"),
    "benchmark": ("libbenchmark-dev",
                  "dpkg-query -W -f='${Package} ${Version}\\n' libbenchmark-dev",
                  r"libbenchmark-dev (\S+)"),
    "boost": ("libboost-dev", "grep -E '#define BOOST_LIB_VERSION' /usr/include/boost/version.hpp",
              r'BOOST_LIB_VERSION "([\d_]+)"'),
}


def main():
    docker.build_base()
    rows = []
    for tool, (pkg, own, rx) in TOOLS.items():
        rc, raw, cmd = docker.run_in_base(own)
        rc2, policy, _ = docker.run_in_base("apt-cache policy %s | grep -m1 Installed" % pkg)
        m = re.search(rx, raw)
        if rc != 0 or not m:
            raise SystemExit("%s: версия не снята (код %d)\n%s" % (tool, rc, raw))
        rows.append({"kind": "tool", "variant": tool, "tool": tool,
                     "declared": policy.split(":", 1)[-1].strip(),
                     "measured": m.group(1), "raw": raw.strip(), "cmd": cmd})
        print("%-10s measured=%-12s declared=%s" % (tool, m.group(1), rows[-1]["declared"]))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")
    read_measurements(FIXTURE)


if __name__ == "__main__":
    main()
