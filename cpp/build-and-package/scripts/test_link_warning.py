"""Предупреждение компоновщика: снято, а не пересказано.

Наблюдение оси упаковки звучит так: у пары glibc-static /
glibc-static-old строка предупреждения компоновщика совпадает дословно,
а исходы разрешения имён различаются. Это ДВА ИЗМЕРЕННЫХ ФАКТА и ничего
сверх них: механизм расхождения здесь не установлен. Записанная до
прогона причинная трактовка совпадения отозвана — почему именно,
печатает разбор, и одна из проверок ниже требует, чтобы он это делал.

Пока эта строка воспроизводилась рукой, она жила в отчёте. Здесь она
читается из fixtures/link.jsonl, и проверки требуют от неё того же, чего
стенд требует от любой своей величины:

  * признак присутствия ЧИТАЕТСЯ ИЗ ВЕЩЕСТВА. Поле
    `link_warning_present` обязано совпадать с тем, что видно в
    перехваченном stderr. Измеритель, отвечающий по объявлению вместо
    вывода, — ровно та болезнь, на которой этот стенд уже спотыкался
    дважды;
  * ОТСУТСТВИЕ ЧЕГО-ЛИБО ДОКАЗЫВАЕТСЯ ТОЛЬКО НАД ЖИВЫМИ ДАННЫМИ. Пустой
    stderr при несостоявшейся линковке не значит «предупреждения нет» —
    он значит «линковать было нечего». Поэтому у каждой строки требуется
    улика того, что линковка шла;
  * ПРИЗНАК ОБЯЗАН РАЗЛИЧАТЬ ПЛЕЧИ. Признак, отвечающий одинаково всем,
    не проверяет ничего;
  * НОРМАЛИЗАЦИЯ НЕ СЪЕДАЕТ СУТЬ. Нормализованная строка обязана
    сохранять и признак, и имя функции, о которой предупреждают.
"""

import io
import json
import os
import subprocess
import sys
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, HERE)

from scripts import link as linklib
from scripts.outcome import read_measurements

# Путь берётся из окружения — так же, как на других осях: диверсия
# работает над КОПИЕЙ фикстуры, а не над файлом репозитория.
FIXTURE = os.environ.get("LINK_FIXTURE",
                         os.path.join(HERE, "fixtures", "link.jsonl"))
IMAGES = os.path.join(HERE, "images")

# Пара про устаревшее правило и контроли предупреждения.
PAIR = ("glibc-static", "glibc-static-old")
WITHOUT_WARNING = ("glibc-dynamic", "musl-static", "distroless")


def rows():
    return {r["variant"]: r for r in read_measurements(FIXTURE)
            if r["kind"] == "link"}


def dockerfiles_of_the_axis():
    """Различные файлы сборки оси — из variants.json, а не рукой.

    Список, выписанный рукой, молча разошёлся бы с осью: вариант,
    добавленный в variants.json, не попал бы в замер, а отсутствие его
    строки выглядело бы как отсутствие предупреждения.
    """
    with io.open(os.path.join(IMAGES, "variants.json"),
                 encoding="utf-8") as handle:
        variants = json.loads(handle.read())
    out = {}
    for variant in variants:
        out.setdefault(variant["dockerfile"], variant["name"])
    return out


class Coverage(unittest.TestCase):
    def test_fixture_exists(self):
        self.assertTrue(os.path.exists(FIXTURE),
                        "нет fixtures/link.jsonl — предупреждение "
                        "компоновщика не снято, и второе наблюдение оси "
                        "держится на памяти")

    def test_every_distinct_dockerfile_is_measured(self):
        measured = rows()
        self.assertTrue(measured, "в фикстуре линковки нет строк")
        by_dockerfile = {r["dockerfile"] for r in measured.values()}
        self.assertEqual(
            sorted(by_dockerfile), sorted(dockerfiles_of_the_axis()),
            "снят не каждый различный файл сборки оси: непокрытый вариант "
            "молча выглядел бы вариантом без предупреждения")


class TheLinkReallyHappened(unittest.TestCase):
    """Отсутствие предупреждения доказуемо только над состоявшейся линковкой."""

    def test_every_row_reports_a_link_step(self):
        for name, row in sorted(rows().items()):
            self.assertTrue(
                row["link_step_ran"],
                "%s: улики линковки нет (%r в stdout не найдено) — пустой "
                "stderr тут значил бы «линковать было нечего», а не "
                "«предупреждения нет»" % (name, linklib.LINK_STEP_MARKER))

    def test_the_link_step_marker_is_really_in_the_captured_stdout(self):
        """Улика тоже читается из вещества, а не из своего же поля."""
        for name, row in sorted(rows().items()):
            self.assertEqual(
                row["link_step_ran"],
                linklib.link_step_ran(row["link_stdout"]),
                "%s: поле link_step_ran разошлось с перехваченным stdout"
                % name)

    def test_every_link_succeeded(self):
        for name, row in sorted(rows().items()):
            self.assertEqual(
                row["link_returncode"], 0,
                "%s: линковка вернула %r — предупреждение осталось "
                "предупреждением только если сборка прошла"
                % (name, row["link_returncode"]))


class TheFlagIsReadFromTheSubstance(unittest.TestCase):
    """Признак присутствия обязан следовать за перехваченным выводом."""

    def test_the_flag_agrees_with_the_captured_stderr(self):
        for name, row in sorted(rows().items()):
            self.assertEqual(
                row["link_warning_present"],
                linklib.has_warning(row["link_stderr"]),
                "%s: link_warning_present=%r, а в перехваченном stderr "
                "признак %r %s. Измеритель отвечает по объявлению, а не по "
                "веществу." % (name, row["link_warning_present"],
                               linklib.MARKER,
                               "есть" if linklib.MARKER in row["link_stderr"]
                               else "отсутствует"))

    def test_the_raw_output_is_kept_whole(self):
        """Сырой вывод лежит рядом: нормализацию можно перепроверить."""
        for name, row in sorted(rows().items()):
            self.assertIn("link_stderr", row)
            if row["link_warning_present"]:
                self.assertIn(
                    linklib.MARKER, row["link_stderr"],
                    "%s: предупреждение объявлено, а сырого вывода с ним "
                    "нет — в фикстуре осталась только пересказанная строка"
                    % name)

    def test_every_warning_line_comes_from_the_raw_output(self):
        for name, row in sorted(rows().items()):
            normalized_raw = linklib.normalize(row["link_stderr"])
            for line in row["link_warning_lines"]:
                self.assertIn(
                    line, normalized_raw,
                    "%s: строка предупреждения %r не встречается в "
                    "нормализованном сыром выводе — она дописана, а не "
                    "снята" % (name, line))

    def test_the_marker_is_recorded_next_to_the_verdict(self):
        """Читатель обязан видеть, ЧТО сопоставлялось."""
        for name, row in sorted(rows().items()):
            self.assertEqual(row["link_warning_marker"], linklib.MARKER,
                             "%s: признак в фикстуре разошёлся с признаком "
                             "измерителя" % name)
            self.assertTrue(str(row["link_warning_normalization"]).strip(),
                            "%s: способ нормализации не записан рядом со "
                            "строкой" % name)


class TheFlagDiscriminates(unittest.TestCase):
    """Признак, отвечающий одинаково всем, не проверяет ничего."""

    def test_the_pair_about_the_rule_both_warn(self):
        measured = rows()
        for name in PAIR:
            self.assertTrue(
                measured[name]["link_warning_present"],
                "%s: предупреждения нет — наблюдение про одинаковую "
                "строку у пары не состоялось" % name)

    def test_the_arms_without_glibc_static_do_not_warn(self):
        measured = rows()
        for name in WITHOUT_WARNING:
            self.assertFalse(
                measured[name]["link_warning_present"],
                "%s: предупреждение появилось там, где статики с glibc нет "
                "— признак отвечает всем одинаково и ничего не различает"
                % name)

    def test_the_verdict_is_not_the_same_everywhere(self):
        verdicts = {r["link_warning_present"] for r in rows().values()}
        self.assertEqual(
            verdicts, {True, False},
            "признак дал один и тот же ответ всем вариантам — он не про "
            "предупреждение, а про что-то постоянное")


class TheSameWarningWithDifferentOutcomes(unittest.TestCase):
    """Само наблюдение: одинаковая строка при разных исходах пары.

    Именно наблюдение, а не причина. Утверждать по нему, что стало с
    причиной предупреждения, нельзя: у плеч пары различается образ
    сборки целиком.
    """

    def test_both_arms_print_the_identical_warning(self):
        measured = rows()
        new, old = measured[PAIR[0]], measured[PAIR[1]]
        self.assertEqual(
            new["link_warning_lines"], old["link_warning_lines"],
            "предупреждение у плеч пары РАЗЛИЧАЕТСЯ (%r против %r) — тогда "
            "наблюдения про одинаковую строку нет вовсе"
            % (new["link_warning_lines"], old["link_warning_lines"]))
        self.assertTrue(new["link_warning_lines"],
                        "у пары нет ни одной строки предупреждения")

    def test_the_normalization_does_not_eat_the_substance(self):
        """Нормализована ровно нумерация, а не текст предупреждения."""
        for name in PAIR:
            for line in rows()[name]["link_warning_lines"]:
                self.assertIn(linklib.MARKER, line,
                              "%s: нормализация съела признак" % name)
                self.assertIn("getaddrinfo", line,
                              "%s: нормализация съела имя функции, о "
                              "которой предупреждают" % name)
                self.assertIn("statically linked", line,
                              "%s: нормализация съела упоминание "
                              "статической линковки" % name)
                self.assertNotIn(
                    "0x0", line,
                    "%s: в нормализованной строке остался адрес — она "
                    "различалась бы у плеч по расположению кода, а не по "
                    "предупреждению" % name)

    def test_the_old_arm_refuses_while_both_warn(self):
        """Связка с осью упаковки: строка одна, исходы разные.

        Без этой проверки два измеренных факта жили бы порознь: пара
        могла бы перестать расходиться по исходу, а печать про
        одинаковую строку при разных исходах осталась бы зелёной,
        потеряв вторую половину наблюдения.
        """
        package = os.environ.get(
            "PACKAGE_FIXTURE",
            os.path.join(HERE, "fixtures", "package.jsonl"))
        outcomes = {r["variant"]: r["outcome"]
                    for r in read_measurements(package)
                    if r.get("probe") == "resolve"}
        self.assertEqual(outcomes.get("glibc-static"), "resolved")
        self.assertEqual(outcomes.get("glibc-static-old"), "refused")
        measured = rows()
        self.assertEqual(
            measured["glibc-static"]["link_warning_present"],
            measured["glibc-static-old"]["link_warning_present"],
            "предупреждение перестало быть одинаковым у пары, исходы "
            "которой различаются — наблюдения, ради которого сделан "
            "замер, больше нет")


# --------------------------------------------------------------------
# Отзыв причинной трактовки: проверяется САМА ПЕЧАТЬ РАЗБОРА.
#
# Читатель приходит на стенд по ссылке из статьи и запускает разбор.
# Что записано в expected.json и что лежит в фикстуре, он может и не
# открыть; печать разбора он видит всегда. Поэтому отзыв обязан быть
# именно в ней — и обязан быть проверен, иначе он снова разъедется с
# тем, что стенд утверждает.

RETRACTION_MARKS = (
    "ОТЗЫВ ТРАКТОВКИ, ЗАПИСАННОЙ ДО ПРОГОНА",
    "ЭТА ТРАКТОВКА ОТОЗВАНА",
    "ОБРАЗОМ СБОРКИ ЦЕЛИКОМ",
    "EAI_SYSTEM",
    "Механизм НЕ УСТАНОВЛЕН",
)

# Формулировки отозванного вывода. Любая из них в печати означает, что
# отзыв не доведён до конца: читатель унесёт причинное утверждение,
# которого замер не даёт.
RETRACTED_WORDINGS = (
    "пережило свою причину",
    "пережила свою причину",
    "пережившее свою причину",
    "причина исчезла",
    "правило верно",
)


def analysis_output():
    """Печать разбора оси упаковки целиком — вместе с кодом возврата.

    Код возврата снимается отдельно от текста: разбор, упавший на
    середине, напечатал бы часть отзыва и выглядел бы зелёным у
    проверки, которая ищет подстроки.
    """
    env = dict(os.environ)
    env["PYTHONIOENCODING"] = "utf-8"
    proc = subprocess.run(
        [sys.executable, os.path.join(HERE, "scripts", "analyze-package.py")],
        capture_output=True, text=True, encoding="utf-8", errors="replace",
        cwd=HERE, env=env)
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


class TheAnalysisCarriesTheRetraction(unittest.TestCase):
    """Отзыв стоит там же, где стоял бы вывод, и вывода там больше нет."""

    def test_the_analysis_prints_the_retraction(self):
        code, out = analysis_output()
        self.assertEqual(code, 0,
                         "разбор оси упаковки не отработал (код %d): %s"
                         % (code, out[-600:]))
        for mark in RETRACTION_MARKS:
            self.assertIn(
                mark, out,
                "в печати разбора нет %r — читатель увидит наблюдение без "
                "отзыва причинной трактовки, записанной до прогона" % mark)

    def test_the_analysis_never_states_the_retracted_cause(self):
        code, out = analysis_output()
        self.assertEqual(code, 0,
                         "разбор оси упаковки не отработал (код %d): %s"
                         % (code, out[-600:]))
        lowered = out.lower()
        for wording in RETRACTED_WORDINGS:
            self.assertNotIn(
                wording, lowered,
                "в печати разбора осталась формулировка отозванного "
                "вывода: %r. Отзыв не доведён до конца — читатель унесёт "
                "причинное утверждение, которого замер не даёт" % wording)


if __name__ == "__main__":
    unittest.main()
