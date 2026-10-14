"""Регрессионный тест предохранителей в разборах фикстур.

Предохранитель, который взводится, но ни на что не влияет, бесполезен — и
именно так было с проверкой расхождения cgroup и PSS в analyze-footprint.py:
она добавляла запись в список проблем, но печать и ненулевой код возврата
стояли выше по коду. Поймало это внешнее ревью, а не я.

Поэтому теперь на каждый предохранитель есть случай, который обязан его
уронить, и случай, который обязан пройти. Коды возврата:

* 0 — замер годен;
* 2 — грязный старт плеча или фикстура старого формата;
* 3 — cgroup и PSS расходятся более чем вдвое (метрика под вопросом);
* 4 — направление расхождения профайлеров непостоянно.

Запуск: python scripts/test_analyze_footprint.py
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
FOOTPRINT = HERE / "analyze-footprint.py"
PROFILE = HERE / "analyze-profile.py"
MODES = ("asyncio", "threads", "processes")
NL = chr(10)


def row(mode, round_no, pss, cgroup, baseline_procs=1, old_format=False):
    r = {
        "build": "gil", "gil_enabled": True, "python": "3.14.7", "mode": mode,
        "concurrency": 64, "count": 2000, "round": round_no,
        "ops_per_sec": 3000.0, "peak_fds": 7, "peak_procs": 1,
        "baseline_fds": 7, "baseline_procs": baseline_procs,
    }
    if old_format:
        r["peak_rss_mb"] = pss
        r["baseline_rss_mb"] = 20.0
    else:
        r.update({"peak_pss_mb": pss, "peak_rss_sum_mb": pss * 1.02,
                  "peak_cgroup_mb": cgroup, "baseline_pss_mb": 20.0,
                  "baseline_cgroup_mb": 30.0})
    return r


def run_analyzer(script, fixtures_dir):
    # encoding задаём явно: на Windows подпроцесс иначе читается в cp1251
    # и кириллица в выводе разбора рушит тест.
    env = dict(os.environ, FIXTURES_DIR=str(fixtures_dir),
               PYTHONIOENCODING="utf-8")
    p = subprocess.run([sys.executable, str(script)], env=env,
                       capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def report(name, code, expect_code, out, expect_text):
    ok = code == expect_code and (expect_text is None or expect_text in out)
    mark = "ОК  " if ok else "СБОЙ"
    print(f"  {mark} {name}: код {code}, ожидался {expect_code}")
    if not ok and expect_text and expect_text not in out:
        print(f"       в выводе нет строки: {expect_text}")
    return ok


def footprint_case(name, rows, expect_code, expect_text=None):
    with tempfile.TemporaryDirectory() as d:
        for build in ("gil", "nogil"):
            path = Path(d) / f"footprint-{build}.txt"
            lines = ["# синтетическая фикстура для теста", "#"]
            for r in rows:
                r = dict(r, build=build, gil_enabled=(build == "gil"))
                lines.append(json.dumps(r, sort_keys=True))
            path.write_text(NL.join(lines) + NL, encoding="utf-8")
        code, out = run_analyzer(FOOTPRINT, d)
    return report(name, code, expect_code, out, expect_text)


def profile_case(name, rounds, expect_code, expect_text=None):
    """Каждый круг задаётся парой (доля по cProfile, доля по py-spy)."""
    with tempfile.TemporaryDirectory() as d:
        lines = ["# синтетическая фикстура для теста", "#"]
        for r, (cp, ps) in enumerate(rounds, 1):
            for mode, val in (("без_профайлера", 1.0), ("cProfile", 1.3),
                              ("py-spy", 1.03)):
                lines.append(f"круг={r} режим={mode} elapsed_s={val}")
            shares = {
                "samples": 700,
                "tiny_step": {"calls": 100, "cprofile_pct": cp,
                              "pyspy_pct": ps},
                "chunky_step": {"calls": 10, "cprofile_pct": 60.0,
                                "pyspy_pct": 70.0},
            }
            lines.append(f"круг={r} доли={json.dumps(shares, sort_keys=True)}")
        (Path(d) / "profile.txt").write_text(NL.join(lines) + NL,
                                             encoding="utf-8")
        code, out = run_analyzer(PROFILE, d)
    return report(name, code, expect_code, out, expect_text)


def main():
    results = []

    print("предохранители analyze-footprint.py:")

    # Чистый набор: cgroup чуть выше PSS, как и должно быть.
    clean = [row(m, r, pss=50.0, cgroup=60.0) for r in (1, 2, 3) for m in MODES]
    results.append(footprint_case("чистый набор проходит", clean, 0,
                                  "ВЕРДИКТ: старт чист"))

    # Грязный старт: одно плечо начало не в одиночестве.
    dirty = [row(m, r, pss=50.0, cgroup=60.0,
                 baseline_procs=3 if (m == "asyncio" and r == 2) else 1)
             for r in (1, 2, 3) for m in MODES]
    results.append(footprint_case("грязный старт отвергается", dirty, 2,
                                  "ЗАГРЯЗНЁН"))

    # Расхождение метрики: PSS 10 МБ против cgroup 100 МБ.
    skewed = [row(m, r, pss=10.0, cgroup=100.0)
              for r in (1, 2, 3) for m in MODES]
    results.append(footprint_case("расхождение cgroup и PSS отвергается",
                                  skewed, 3, "МЕТРИКА ПОД ВОПРОСОМ"))

    # Фикстура старого формата, без PSS.
    old = [row(m, r, pss=50.0, cgroup=0.0, old_format=True)
           for r in (1, 2, 3) for m in MODES]
    results.append(footprint_case("старый формат отвергается", old, 2,
                                  "ЗАГРЯЗНЁН"))

    print()
    print("предохранители analyze-profile.py:")

    results.append(profile_case(
        "постоянное направление проходит",
        [(18.0, 15.0), (18.5, 15.5), (18.2, 15.2)], 0))
    results.append(profile_case(
        "непостоянное направление отвергается",
        [(18.0, 15.0), (14.0, 16.0), (18.2, 15.2)], 4, "НЕГОДЕН"))

    print()
    failed = results.count(False)
    if not failed:
        print(f"ВСЕ {len(results)} ПРОВЕРКИ ПРОШЛИ")
        return 0
    print(f"ПРОВАЛЕНО: {failed} из {len(results)}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
