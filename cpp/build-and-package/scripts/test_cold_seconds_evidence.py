"""Межсессионный разброс времён — утверждение, у которого есть данные.

ЗАЧЕМ ЭТА ПРОВЕРКА.

Ось сборки не публикует кратность холодной сборки: «пара прогонов не
оценивает её разброс». Проверить это по фикстурам репозитория было
НЕЛЬЗЯ — там лежит одна сессия, то есть одна пара, а по одной паре
нельзя судить, представительна она или нет. Утверждение держалось на
памяти автора, то есть ровно на том, против чего стенд и построен.
Теперь рядом лежит `fixtures/evidence/cold-seconds-by-session.json`,
собранный `scripts/run-evidence.py` из истории фикстур, и эта проверка
следит, чтобы он не разошёлся с историей и чтобы утверждение по нему
действительно читалось.

ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ БЕЗ GIT, А ЧТО ТОЛЬКО С НИМ.

Без git: что сессий несколько, что сравнимых (снятых на одном окне
замера) хотя бы две и что пара прогонов не оценивает разброс величины —
общий разброс по всем сессиям шире расхождения внутри самой широкой
пары, а самая согласная пара уже этого разброса в разы, — то есть само
утверждение. С git: что числа сессий совпадают с фикстурами тех
коммитов, из которых они взяты, и что свежий коммит фикстур не забыт.
Без истории (копия стенда, распакованный архив) вторая половина
пропускается — и говорит об этом вслух.
"""

import importlib.util
import io
import json
import os
import subprocess
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EVIDENCE = os.environ.get(
    "COLD_EVIDENCE",
    os.path.join(HERE, "fixtures", "evidence", "cold-seconds-by-session.json"))


def load():
    with io.open(EVIDENCE, encoding="utf-8") as handle:
        return json.loads(handle.read())


def evidence_module():
    path = os.path.join(HERE, "scripts", "run-evidence.py")
    spec = importlib.util.spec_from_file_location("run_evidence", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def comparable(evidence):
    """Сессии, снятые на одном и том же окне замера.

    Самая ранняя сессия снята до выравнивания окна: у её системного
    плеча зависимость получалась вне замера, и кратность там — другая
    величина под тем же именем. Сравнивать её с прочими нельзя.
    """
    return [s for s in evidence["sessions"]
            if set(s["measurement_window"].values()) == {"dependency_inside"}]


def ratios(session, arm, base="system"):
    return [values[arm] / values[base]
            for _, values in sorted(session["cold_seconds"].items())
            if values.get(base)]


def has_git():
    try:
        proc = subprocess.run(["git", "rev-parse", "--is-inside-work-tree"],
                              capture_output=True, text=True,
                              encoding="utf-8", errors="replace", cwd=HERE)
    except (OSError, ValueError):
        return False
    return proc.returncode == 0 and proc.stdout.strip() == "true"


class Evidence(unittest.TestCase):
    def test_the_evidence_file_exists(self):
        self.assertTrue(
            os.path.exists(EVIDENCE),
            "нет fixtures/evidence/cold-seconds-by-session.json: утверждение "
            "о межсессионном разбросе нечем подтвердить. Собирается "
            "`python3 scripts/run-evidence.py`.")

    def test_at_least_two_comparable_sessions(self):
        sessions = comparable(load())
        self.assertGreaterEqual(
            len(sessions), 2,
            "сравнимых сессий меньше двух: о разбросе МЕЖДУ сессиями "
            "говорить не с чего, и утверждение README держится на памяти")

    def test_a_pair_of_runs_does_not_estimate_the_spread(self):
        """Само утверждение: пара прогонов не оценивает разброс величины.

        ЭТА ПРОВЕРКА УЖЕ ОДИН РАЗ ПОКРАСНЕЛА — И ЭТО БЫЛА НАХОДКА.
        Первая её редакция требовала «между сессиями шире, чем внутри
        сессии»: на трёх сессиях так и было (пара расходилась на 0,1–1,6
        %, сессии между собой — на 6,4 %). Четвёртая сессия дала пару
        1,581 и 1,693, то есть 7,1 % внутри себя — шире, чем расходятся
        сессии. Формулировка была переписана по данным, а не данные
        подогнаны под формулировку.

        Что проверяется теперь, по плечу conan:

          * общий разброс по ВСЕМ прогонам всех сравнимых сессий шире,
            чем расхождение внутри самой широкой пары, — то есть ни одна
            пара не покрывает разброс величины;
          * самая согласная пара уже общего разброса в разы — то есть
            пара прогонов может показать «устойчивость», которой у
            величины нет.

        Вывод из обоих: кратность холодной сборки в статью не идёт, и
        пара прогонов этого вывода не меняет.
        """
        sessions = comparable(load())
        all_ratios = [r for s in sessions for r in ratios(s, "conan")]
        total = 100.0 * (max(all_ratios) - min(all_ratios)) / min(all_ratios)
        pairs = [100.0 * (max(ratios(s, "conan")) - min(ratios(s, "conan")))
                 / min(ratios(s, "conan")) for s in sessions]
        self.assertGreater(
            total, max(pairs),
            "общий разброс кратности (%.1f %%) не шире самой широкой пары "
            "(%.1f %%): утверждение «пара прогонов не покрывает разброс "
            "величины» этими данными не подтверждается"
            % (total, max(pairs)))
        self.assertGreater(
            total, 3.0 * min(pairs),
            "самая согласная пара разошлась на %.1f %% при общем разбросе "
            "%.1f %%: пара перестала выглядеть обманчиво устойчивой, и "
            "утверждение о ней надо перечитать" % (min(pairs), total))

    def test_every_session_says_where_it_came_from(self):
        evidence = load()
        self.assertTrue(evidence["sessions"], "в свидетельстве нет сессий")
        for session in evidence["sessions"]:
            self.assertTrue(str(session.get("source", "")).strip(),
                            "сессия %r не говорит, откуда взята"
                            % session.get("label"))
            self.assertTrue(session["cold_seconds"],
                            "сессия %r без времён" % session.get("label"))
            self.assertTrue(session["measurement_window"],
                            "сессия %r не называет окно замера"
                            % session.get("label"))

    def test_recorded_sessions_match_the_history_they_name(self):
        """Числа сессий обязаны совпадать с фикстурами их коммитов.

        Иначе свидетельство — такой же пересказ по памяти, как и то, что
        оно призвано заменить.
        """
        if not has_git():
            self.skipTest("git недоступен или это не рабочее дерево "
                          "репозитория: сверить сессии с историей нечем")
        module = evidence_module()
        from_git = {s["commit"]: s for s in module.from_git()}
        for session in load()["sessions"]:
            commit = session.get("commit")
            if not commit:
                continue
            self.assertIn(commit, from_git,
                          "в истории нет коммита %s, названного в "
                          "свидетельстве" % commit)
            self.assertEqual(
                session["cold_seconds"], from_git[commit]["cold_seconds"],
                "времена сессии %s разошлись с фикстурами её коммита" % commit)

    def test_the_full_run_rebuilds_the_evidence_before_it_checks(self):
        """Свежесть свидетельства обязан обеспечивать сам прогон.

        Проверка свежести выше ловит протухшее свидетельство, но лечить
        его руками нельзя: о ручном шаге забывают, и это не гипотеза —
        так и вышло. Прогон ставил маркер, фикстуры коммитились, и
        СЛЕДУЮЩИЙ полный прогон падал на свежести, потому что последний
        коммит фикстур в свидетельство никто не внёс. Маркер не
        ставился, стенд выглядел непроверенным.

        Поэтому порядок шагов в bench/run-all.sh — предмет проверки, а
        не договорённость: пересборка свидетельства обязана стоять ДО
        разборов (они его читают) и ДО набора проверок (он его сверяет).
        """
        path = os.path.join(HERE, "bench", "run-all.sh")
        with io.open(path, encoding="utf-8") as handle:
            script = handle.read()

        where_evidence = script.find("scripts/run-evidence.py")
        self.assertNotEqual(
            where_evidence, -1,
            "в bench/run-all.sh нет шага пересборки свидетельства: "
            "свежесть снова придётся поддерживать руками")

        for what, needle in (("разбор сборки", "scripts/analyze-build.py"),
                             ("набор проверок", "unittest discover")):
            where = script.find(needle)
            self.assertNotEqual(where, -1,
                                "в прогоне не нашёлся шаг «%s»" % what)
            self.assertLess(
                where_evidence, where,
                "пересборка свидетельства стоит ПОСЛЕ шага «%s»: он "
                "прочитает прошлое состояние" % what)


    def test_the_newest_fixtures_are_represented(self):
        """Свежий прогон не должен остаться за пределами свидетельства.

        Ровно так протухал прежний маркер прогона: файл лежал, состояние
        ушло вперёд, и никто этого не читал.
        """
        if not has_git():
            self.skipTest("git недоступен: свежесть свидетельства сверить "
                          "не с чем")
        module = evidence_module()
        newest = module.from_git()[-1]
        recorded = [s["cold_seconds"] for s in load()["sessions"]]
        self.assertIn(
            newest["cold_seconds"], recorded,
            "последний коммит фикстур (%s) в свидетельстве не отражён: "
            "пересоберите его — `python3 scripts/run-evidence.py`"
            % newest["label"])


if __name__ == "__main__":
    unittest.main()
