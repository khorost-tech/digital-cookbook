#!/usr/bin/env python3
"""Свидетельство межсессионного разброса времён сборки.

ЗАЧЕМ ЭТО ЕСТЬ.

Центральное методическое утверждение оси сборки — «кратность холодной
сборки в статью не идёт, потому что пара прогонов не оценивает её
разброс». До этого файла проверить его было НЕЧЕМ: фикстуры
перезаписываются каждым прогоном, а в репозитории лежит только
последняя сессия — одна пара прогонов, по которой нельзя увидеть ни
разброс между сессиями, ни то, представительна ли эта пара. Читатель,
пришедший по ссылке, увидел бы одну пару чисел и никак не смог бы
проверить, устойчивость это величины или свойство именно этой пары —
утверждение держалось бы только на памяти автора, то есть ровно на том,
против чего стенд и построен.

ЧТО ЗДЕСЬ СОБИРАЕТСЯ.

Времена холодной сборки по СЕССИЯМ. Прошлые сессии берутся из истории
git: фикстуры коммитятся, и каждый коммит `fixtures/build.jsonl` — это
отдельный прогон на отдельной машинной сессии. Текущая сессия берётся из
рабочего дерева. Числа не переписываются руками ни на одном шаге.

ЧЕГО ЭТОТ ФАЙЛ НЕ ДОКАЗЫВАЕТ.

Ничего о причине разброса: что именно менялось между сессиями (тепловое
состояние машины, состояние кэшей Docker, соседняя нагрузка), стенд не
мерил и назвать не может. Утверждение здесь только одно и узкое: общий
разброс кратности по всем сессиям шире, чем расхождение внутри самой
широкой отдельной пары, а самая согласная пара может быть уже этого
разброса в разы — то есть пара прогонов не оценивает разброс величины.

ЗАПУСК (нужен git и история репозитория):

    python3 scripts/run-evidence.py
"""

import io
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts.console import force_utf8  # noqa: E402

force_utf8()

FIXTURES = os.path.join(HERE, "fixtures")
EVIDENCE_DIR = os.path.join(FIXTURES, "evidence")
EVIDENCE_PATH = os.path.join(EVIDENCE_DIR, "cold-seconds-by-session.json")

# Обе фикстуры оси сборки: пара прогонов ОДНОЙ сессии.
RUNS = (("run-1", "build.run-1.jsonl"), ("run-2", "build.jsonl"))
ARMS = ("system", "vcpkg", "conan")


def _git(args):
    proc = subprocess.run(["git"] + args, capture_output=True, text=True,
                          encoding="utf-8", errors="replace", cwd=HERE)
    if proc.returncode != 0:
        raise SystemExit("git %s: %s" % (" ".join(args), proc.stderr.strip()))
    return proc.stdout


def _rows(text, source):
    rows = [json.loads(l) for l in text.split("\n")
            if l.strip().startswith("{")]
    if not rows:
        raise SystemExit("%s: нет строк данных" % source)
    if "COMPLETE" not in text:
        raise SystemExit("%s: фикстура оборвана" % source)
    return {r["variant"]: r for r in rows}


def _session(label, tables, extra):
    """Одна сессия: времена холодной сборки обоих её прогонов."""
    session = dict(extra)
    session["label"] = label
    session["cold_seconds"] = {}
    session["measurement_window"] = {}
    for run, table in tables.items():
        session["cold_seconds"][run] = {
            arm: table[arm]["cold_seconds"] for arm in ARMS if arm in table}
        for arm in ARMS:
            if arm in table:
                # У самой ранней сессии поля «окно замера» ещё не было:
                # ось выравнивали по окну уже после неё. Пропуск здесь
                # не молчаливый — окно так и записывается словами, и
                # сравнивать кратности этой сессии с прочими нельзя.
                session["measurement_window"][arm] = table[arm].get(
                    "measurement_window", "не записано")
    return session


def from_git():
    """Сессии из истории: каждый коммит фикстуры — отдельный прогон."""
    root = _git(["rev-parse", "--show-toplevel"]).strip()
    prefix = os.path.relpath(HERE, root).replace("\\", "/") + "/fixtures/"
    log = _git(["log", "--follow", "--reverse",
                "--format=%h\t%cI\t%s", "--",
                "fixtures/build.jsonl"]).strip()
    sessions = []
    for line in log.split("\n"):
        if not line.strip():
            continue
        sha, when, subject = line.split("\t", 2)
        tables = {}
        for run, name in RUNS:
            text = _git(["show", "%s:%s%s" % (sha, prefix, name)])
            tables[run] = _rows(text, "%s:%s" % (sha, name))
        sessions.append(_session(
            sha, tables,
            {"source": "git", "commit": sha, "committed_at": when,
             "commit_subject": subject}))
    return sessions


def from_worktree():
    tables = {}
    for run, name in RUNS:
        with io.open(os.path.join(FIXTURES, name), encoding="utf-8") as handle:
            tables[run] = _rows(handle.read(), name)
    return _session("рабочее дерево", tables,
                    {"source": "рабочее дерево",
                     "commit": None, "committed_at": None,
                     "commit_subject": None})


def ratios(session, arm, base="system"):
    """Кратности плеча к системному по каждому прогону сессии."""
    out = {}
    for run, values in sorted(session["cold_seconds"].items()):
        if arm in values and base in values and values[base]:
            out[run] = values[arm] / values[base]
    return out


def main():
    sessions = from_git()
    current = from_worktree()
    if not sessions or sessions[-1]["cold_seconds"] != current["cold_seconds"]:
        sessions.append(current)
    else:
        # Рабочее дерево совпало с последним коммитом — отдельной сессией
        # оно не является, и записывать его второй раз значило бы удвоить
        # одну сессию.
        sessions[-1]["source"] = "git; совпадает с рабочим деревом"

    evidence = {
        "what_this_is":
            "Времена холодной сборки оси 2 по СЕССИЯМ. Прошлые сессии "
            "прочитаны из истории git (каждый коммит fixtures/build.jsonl "
            "— отдельный прогон), текущая — из рабочего дерева. Файл "
            "собирается scripts/run-evidence.py; руками числа сюда не "
            "переносились.",
        "why_it_exists":
            "Кратность холодной сборки в README и в статью не идёт: пара "
            "прогонов не оценивает её разброс. Фикстуры репозитория несут "
            "ОДНУ сессию — одну пару чисел, — и по ней одной нельзя "
            "проверить ни разброс между сессиями, ни то, представительна "
            "ли эта пара: у самой этой сессии пара может разойтись почти "
            "так же широко, как расходятся сессии между собой. Без этого "
            "файла утверждение держалось бы на памяти автора.",
        "what_it_does_not_show":
            "Причину разброса. Что менялось между сессиями — тепловое "
            "состояние машины, состояние кэшей Docker, соседняя "
            "нагрузка — стенд не мерил.",
        "arms": list(ARMS),
        "runs_per_session": [run for run, _ in RUNS],
        "sessions": sessions,
    }
    if not os.path.isdir(EVIDENCE_DIR):
        os.makedirs(EVIDENCE_DIR)
    with io.open(EVIDENCE_PATH, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(json.dumps(evidence, ensure_ascii=False, indent=2,
                                sort_keys=True) + "\n")

    print("свидетельство записано: %s" % EVIDENCE_PATH)
    print("сессий: %d" % len(sessions))
    for session in sessions:
        window = set(session["measurement_window"].values())
        print("  %-12s окно замера: %s"
              % (session["label"], ", ".join(sorted(window))))
        for arm in ("conan", "vcpkg"):
            got = ratios(session, arm)
            if got:
                print("      %s/system: %s"
                      % (arm, ", ".join("%.3f" % v
                                        for _, v in sorted(got.items()))))
    return 0


if __name__ == "__main__":
    sys.exit(main())
