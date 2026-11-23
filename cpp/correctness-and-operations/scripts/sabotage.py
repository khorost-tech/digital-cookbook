"""Реестр диверсий: каждая проверка обязана краснеть от своей поломки.

Прогон: копия стенда во временном каталоге — с `cpp/_common` на том же месте
относительно стенда, иначе импорты копии разрешились бы в оригинал. Сначала
контрольная копия без диверсий: её набор обязан пройти; иначе «покраснело»
у диверсий ничего не значит. Затем каждая диверсия по одной: применить замену
текста, прогнать набор, ожидать провал. Неприменившаяся диверсия (искомого
текста нет) — проваленная проверка, а не пропуск.

    python3 scripts/sabotage.py            → по одной строке на диверсию, код 0 — все сработали

Пока ставится маркер (STAND_MARKER_PENDING=1, run-marker.py), диверсии,
нацеленные на сам маркер, пропускаются и называются отдельно: маркера ещё
нет, применяться не к чему. Отдельный запуск реестра гоняет их все.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COMMON = os.path.join(os.path.dirname(HERE), "_common")

# (имя, файл относительно стенда, было, стало). «Было» с приставкой re: —
# регулярное выражение, заменяется первое совпадение; с приставкой all: —
# заменяются все вхождения текста.
SABOTAGES = [
    # задача 1 — версии
    ("версия не из сырого вывода", "fixtures/tools.jsonl",
     '"measured": "14.2.0"', '"measured": "14.2.1"'),
    ("инструмент выкинут из фикстуры", "fixtures/tools.jsonl",
     '"variant": "boost", "tool": "boost"', '"variant": "gcc", "tool": "gcc"'),
    ("дайджест в Dockerfile без base.json", "images/Dockerfile.base",
     "@sha256:d7e1", "@sha256:0000"),
    # задача 2 — ось 1
    ("error превращается в missed", "scripts/analyze-defects.py",
     'return {"outcome": "error", "detail": "детектор не запустился',
     'return {"outcome": "missed", "detail": "детектор не запустился'),
    ("defect_present константой", "scripts/presence.py",
     '    if program == "clean":\n        return None\n',
     '    if program == "clean":\n        return None\n    return True\n'),
    ("чистая программа выкинута из контроля", "scripts/analyze-defects.py",
     '    if not clean:\n        raise SystemExit',
     '    if False:\n        raise SystemExit'),
    ("дефект отсутствует в -O0 по фикстуре", "fixtures/defects.jsonl",
     '"program": "leak", "detector": "valgrind", "opt": "O0", "compiler": "g++", "defect_present": true',
     '"program": "leak", "detector": "valgrind", "opt": "O0", "compiler": "g++", "defect_present": false'),
    ("детектор сработал на чистой программе", "fixtures/defects.jsonl",
     '{"kind": "defect", "variant": "clean", "probe": "asan-O0", "outcome": "missed"',
     '{"kind": "defect", "variant": "clean", "probe": "asan-O0", "outcome": "caught"'),
    ("сборка ASan без -fsanitize", "scripts/detect.sh",
     "-fsanitize=address -fno-omit-frame-pointer $src", "-fno-omit-frame-pointer $src"),
    ("libFuzzer с нулевым временем", "scripts/detect.sh",
     "-max_total_time=30", "-max_total_time=0"),
    # задача 3 — ось 2
    ("работа без проверки вложенного цикла", "scripts/analyze-bench.py",
     "\n              and any(ls != lw and ls[0] <= lw[0] and lw[1] <= ls[1] for ls in loops)]", "]"),
    ("нет символа читается как пустое тело", "scripts/analyze-bench.py",
     'return {"outcome": "error", "detail": "символ %s не найден" % symbol,',
     'return {"outcome": "lost", "detail": "символ %s не найден" % symbol,'),
    ("-O0 потерял работу по фикстуре", "fixtures/bench.jsonl",
     '"symbol_found": true, "work_present": true', '"symbol_found": true, "work_present": false'),
    ("пустая функция с работой по фикстуре", "fixtures/bench.jsonl",
     '"work_present": false', '"work_present": true'),
    # задача 4 — ось 3
    ("сигнал до того, как запросы в полёте", "fixtures/shutdown.jsonl",
     r're:("exited_by_itself": true, "exit_after_signal_s": [\d.]+, "sent": 4, "in_flight_at_signal": )4',
     r"\g<1>0"),
    ("dropped не считается", "fixtures/shutdown.jsonl",
     '"served": 0, "dropped": 4', '"served": 0, "dropped": 0'),
    ("обработчик без закрытия приёма", "service/main.cpp",
     "            acceptor.close(ignored);                     // новых не принимаем\n", ""),
    ("учёт не снимается по подтверждению ядром клиента", "service/main.cpp",
     "            --in_flight;                        // ответ в ядре клиента — дослужен\n", ""),
    ("недетерминированная клетка матрицы не помечена", "fixtures/defects.jsonl",
     r're:("probe": "gtest-O0".*?"nondeterministic": )true', r"\g<1>false"),
    ("недетерминированный вариант не помечен", "fixtures/shutdown.jsonl",
     r're:("variant": "drain-slow-after-write".*?"nondeterministic": )true', r"\g<1>false"),
    ("баланс не сверяется", "scripts/analyze-shutdown.py",
     '        if r["served"] + r["dropped"] != r["sent"]:\n            raise SystemExit',
     '        if False:\n            raise SystemExit'),
    # задача 5 — ось 4
    ("строгий разбор без проверки end", "service/config.cpp",
     "error != std::errc{} || end != text.data() + text.size()", "error != std::errc{}"),
    ("диапазон не проверяется", "service/config.cpp",
     "    if (value < lo || value > hi)\n", "    if (false)\n"),
    ("started-silently засчитан как served", "fixtures/config.jsonl",
     '"outcome": "started-silently"', '"outcome": "served"'),
    ("входы с пробелом и плюсом выкинуты", "fixtures/config.jsonl",
     'all:"input_name": "leading-space"', '"input_name": "space-gone"'),
    # задача 5.1 — ось 5
    ("счёт из ответа сервиса, а не из /proc", "fixtures/fds.jsonl",
     '"source": "/proc/1/fd"', '"source": "service"'),
    ("контроль без ошибок теряет дескрипторы", "fixtures/fds.jsonl",
     '"fd_before": 9, "fd_after": 9, "delta": 0, "responses": {"200": 100}',
     '"fd_before": 9, "fd_after": 12, "delta": 3, "responses": {"200": 100}'),
    ("число ответов с ошибкой не сверяется с M", "scripts/analyze-fds.py",
     "        if got != expected:\n", "        if False:\n"),
    # задача 5.2 — ось 6
    ("разбор принимает любую строку", "scripts/analyze-logs.py",
     '            obj = json.loads(line.decode("utf-8", "surrogateescape"))',
     '            obj = {"msg": ""}'),
    ("контроль без обычного сообщения", "fixtures/logs.jsonl",
     'all:"message": "plain"', '"message": "plain-gone"'),
    # задача 5.3 — ось 7
    ("контрольный тест не различает варианты", "fixtures/coverage.jsonl",
     '"planted": "on", "cmd": "bash scripts/coverage.sh on boundary", "rc": 1, "boundary_passed": false',
     '"planted": "on", "cmd": "bash scripts/coverage.sh on boundary", "rc": 0, "boundary_passed": true'),
    ("проценты не из export", "fixtures/coverage.jsonl",
     '"summary": {"lines": 100, "branches": 75, "mcdc": 50', '"summary": {"lines": 100, "branches": 100, "mcdc": 100'),
    ("основной набор упал", "fixtures/coverage.jsonl",
     '"suite_rc": 0, "suite_passed": true', '"suite_rc": 1, "suite_passed": false'),
    # задача 6 — маркер и сводка
    ("сумма фикстуры без пересчёта", "fixtures/RUN-COMPLETE",
     're:("defects.jsonl": ")[0-9a-f]', r"\g<1>0"),
    ("число проверок вписано, а не посчитано", "fixtures/RUN-COMPLETE",
     r're:"tests": \d+', '"tests": 1'),
    ("проглоченный код возврата", "bench/run-all.sh", "set -euo pipefail", "set -uo pipefail"),
    ("маркер до проверок", "bench/run-all.sh",
     "python3 scripts/run-manifest.py\npython3 scripts/run-marker.py",
     "python3 scripts/run-marker.py\npython3 scripts/run-manifest.py"),
    ("исключённые поля подкручены под совпадение", "scripts/run-manifest.py",
     '    "config": ["effective_port@empty", "effective_port@letters"],', '    "config": [],'),
]


def make_copy(tmp):
    root = os.path.join(tmp, "repo")
    stand = os.path.join(root, "cpp", os.path.basename(HERE))
    shutil.copytree(HERE, stand, ignore=shutil.ignore_patterns("__pycache__"))
    shutil.copytree(COMMON, os.path.join(root, "cpp", "_common"),
                    ignore=shutil.ignore_patterns("__pycache__"))
    with open(os.path.join(root, "cpp", "__init__.py"), "w"):
        pass
    return stand


def run_suite(stand):
    env = dict(os.environ)
    env.pop("STAND_WITH_DOCKER", None)
    proc = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "scripts",
                           "-p", "test_*.py"], cwd=stand, env=env,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return proc.returncode, proc.stdout.decode("utf-8", "replace")


def main():
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        rc, out = run_suite(make_copy(os.path.join(tmp, "control")))
        if rc != 0:
            print("КОНТРОЛЬНАЯ КОПИЯ НЕ ПРОШЛА — диверсии не значат ничего\n" + out[-3000:])
            return 1
        print("контрольная копия: зелёная")
        pending = os.environ.get("STAND_MARKER_PENDING") == "1"
        skipped = 0
        for i, (name, rel, old, new) in enumerate(SABOTAGES):
            if pending and rel == "fixtures/RUN-COMPLETE":
                print("пропущена (маркер ставится): %s" % name)
                skipped += 1
                continue
            stand = make_copy(os.path.join(tmp, "s%d" % i))
            path = os.path.join(stand, rel)
            with open(path, encoding="utf-8") as f:
                text = f.read()
            if old.startswith("re:"):
                changed, n = re.subn(old[3:], new, text, count=1)
            elif old.startswith("all:"):
                changed, n = text.replace(old[4:], new), text.count(old[4:])
            else:
                changed, n = text.replace(old, new, 1), text.count(old)
            if n == 0:
                print("НЕ ПРИМЕНИЛАСЬ: %s (%s)" % (name, rel))
                failures += 1
                continue
            with open(path, "w", encoding="utf-8") as f:
                f.write(changed)
            rc, _ = run_suite(stand)
            ok = rc != 0
            failures += 0 if ok else 1
            print("%s: %s" % ("покраснело" if ok else "ОСТАЛОСЬ ЗЕЛЁНЫМ", name))
    print("диверсий: %d, не сработало: %d" % (len(SABOTAGES) - skipped, failures))
    if skipped:
        print("пропущено на время постановки маркера: %d" % skipped)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
