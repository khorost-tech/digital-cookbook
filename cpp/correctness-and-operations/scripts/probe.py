"""Задача 1: пробы инструментов ДО записи ожиданий → fixtures/probe.jsonl.

Всё, что здесь выяснено, — известное, а не предсказание: в expected.json оно
идёт в `known_from_pre_run_probes` со ссылкой на строку этой фикстуры.

Каждая проба — одна команда в базовом образе. Исход решается по выводу
инструмента (`observed`/`absent`), а `error` — только когда не удалось
собрать или запустить саму пробу. Код возврата пишется отдельно: у
санитайзеров и фаззера ненулевой код — ожидаемая часть находки, а не поломка.
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, PROBE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "probe.jsonl")

CP = "mkdir -p /tmp/p && cd /tmp/p && "

PROBES = [
    # (инструмент, проба, сценарий, privileged, regex находки)
    ("libfuzzer", "known-crash",
     CP + "clang++ -g -fsanitize=fuzzer,address /src/probes/fuzz_known.cpp -o fz"
          " && ./fz -seed=1 -runs=5000000 2>&1; ls crash-*",
     False, r"^crash-[0-9a-f]+"),
    ("perf", "stacks-O2-fp",
     CP + "g++ -O2 -g -fno-omit-frame-pointer /src/probes/stacks.cpp -o st"
          " && perf record -q -F 999 -g -o p.data ./st >/dev/null 2>&1"
          " && perf report -i p.data --stdio --no-children 2>/dev/null"
          " | grep -E -m1 -A6 '\\[\\.\\] leaf'",
     True, r"mid"),
    ("perf", "stacks-O2-nofp",
     CP + "g++ -O2 -g /src/probes/stacks.cpp -o st"
          " && perf record -q -F 999 -g -o p.data ./st >/dev/null 2>&1"
          " && perf report -i p.data --stdio --no-children 2>/dev/null"
          " | grep -E -m1 -A6 '\\[\\.\\] leaf'",
     True, r"mid"),
    ("perf", "hw-instructions",
     CP + "g++ -O2 /src/probes/stacks.cpp -o st && perf stat -e instructions ./st 2>&1"
          " | grep -E 'instructions'",
     True, r"^\s*[\d,]+\s+instructions"),
    ("asan+gtest", "heap-overflow",
     CP + "g++ -g -fsanitize=address /src/probes/gtest_sanitizers.cpp -lgtest -lgtest_main -o t"
          " && ./t --gtest_filter=Probe.HeapOverflow 2>&1",
     False, r"AddressSanitizer: heap-buffer-overflow"),
    ("ubsan+gtest", "signed-overflow",
     CP + "g++ -g -fsanitize=undefined -fno-sanitize-recover=all /src/probes/gtest_sanitizers.cpp"
          " -lgtest -lgtest_main -o t && ./t --gtest_filter=Probe.SignedOverflow 2>&1",
     False, r"signed integer overflow"),
    ("tsan+gtest", "race",
     CP + "g++ -g -O1 -fsanitize=thread /src/probes/gtest_sanitizers.cpp -lgtest -lgtest_main -o t"
          " && ./t --gtest_filter=Probe.Race 2>&1",
     False, r"ThreadSanitizer: data race"),
    ("plain+gtest", "all-green",
     CP + "g++ -g /src/probes/gtest_sanitizers.cpp -lgtest -lgtest_main -o t && ./t 2>&1"
          " | grep -E 'PASSED|FAILED'",
     False, r"PASSED.*3 tests"),
    ("llvm-cov", "mcdc",
     CP + "clang++ -fprofile-instr-generate -fcoverage-mapping -fcoverage-mcdc"
          " /src/probes/cover.cpp -o c && LLVM_PROFILE_FILE=c.profraw ./c >/dev/null"
          " && llvm-profdata merge c.profraw -o c.profdata"
          " && llvm-cov report ./c -instr-profile=c.profdata --show-mcdc-summary 2>&1",
     False, r"MC/DC"),
    ("benchmark", "json",
     CP + "g++ -O2 /src/probes/bench.cpp -lbenchmark -lpthread -o b"
          " && ./b --benchmark_format=json --benchmark_min_time=0.01s 2>/dev/null"
          " | grep -E '\"name\"|\"iterations\"'",
     False, r'"name": "BM_Sum"'),
]


def main():
    docker.build_base()
    versions, vcmd = docker.tool_versions()
    rows = [{"kind": "versions", "variant": "base", "probe": "tool_versions",
             "outcome": "observed", "detail": json.dumps(versions, ensure_ascii=False),
             "cmd": vcmd}]
    for tool, probe, script, priv, pattern in PROBES:
        rc, out, cmd = docker.run_in_base(script, privileged=priv)
        found = re.search(pattern, out, re.M) is not None
        if found:
            outcome, detail = "observed", "найдено: %s" % pattern
        elif rc != 0 and not out.strip():
            outcome, detail = "error", "код %d без вывода" % rc
        else:
            outcome, detail = "absent", "нет совпадения с %s (код %d)" % (pattern, rc)
        rows.append({"kind": "probe", "variant": tool, "probe": probe,
                     "outcome": outcome, "detail": detail, "cmd": cmd,
                     "rc": rc, "output": out[-4000:]})
        print("%-12s %-18s %-9s rc=%d" % (tool, probe, outcome, rc))
    os.makedirs(os.path.dirname(FIXTURE), exist_ok=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            line = json.dumps(row, ensure_ascii=False)
            PROBE.parse_line(line)
            f.write(line + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
