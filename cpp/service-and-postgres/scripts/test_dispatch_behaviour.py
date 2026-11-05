"""Поведение общего разбора запроса и ручки числа рабочих единиц.

ЗАЧЕМ ОТДЕЛЬНО ОТ ПРОВЕРОК ТЕКСТА. Текст держит форму, и форму обходят.
`index < workers + 1` стоит ровно там, где проверка ищет `workers`, и
проходит зелёным; чтение окружения, обёрнутое в try/catch с запасным
значением, оставляет на месте и вызов, и его место до открытия порта.
Ответ на конкретный вход обойти нечем — поэтому здесь логика СОБИРАЕТСЯ и
ЗАПУСКАЕТСЯ (`service/dispatch_probe.cpp`).

ПОЧЕМУ БЕЗ БАЗЫ И БЕЗ ДОКЕРА. Всё, что здесь спрашивается, решается до
похода в базу: маршрут, разбор параметра, отказ на кривом входе, чтение
числа рабочих единиц. Требовать PostgreSQL или докер значило бы сделать
проверку непрогоняемой там, где она нужнее всего, — на машине разбора.

ЕСЛИ СОБРАТЬ НЕЧЕМ — проверка объявляет себя ПРОПУЩЕННОЙ с причиной, а не
пройденной: зелёный результат означал бы, что поведение проверено.
"""

import os
import shutil
import subprocess
import tempfile
import unittest

from scripts.test_service_behaviour import _compiler

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SERVICE = os.path.join(STAND, "service")


class DispatchBehaviour(unittest.TestCase):
    def setUp(self):
        compiler = _compiler()
        if compiler is None:
            self.skipTest("нечем собрать: компилятор C++ не найден")
        self.work = tempfile.mkdtemp(prefix="dispatch-probe-")
        self.addCleanup(shutil.rmtree, self.work, True)
        self.binary = os.path.join(self.work, "probe")
        failure_text = ""
        # Стандарт подбирается по той же причине, что и в пробе логики:
        # libpqxx машины разбора собрана своим стандартом, и несовпадение
        # ломает не компиляцию, а линковку. Это свойство машины, а не
        # дефект стенда.
        for standard in ("c++20", "c++17"):
            build = subprocess.run(
                [compiler, "-std=" + standard, "-I", SERVICE,
                 os.path.join(SERVICE, "main.cpp"),
                 os.path.join(SERVICE, "dispatch.cpp"),
                 os.path.join(SERVICE, "dispatch_probe.cpp"),
                 "-o", self.binary, "-lpqxx", "-lpq", "-pthread"],
                capture_output=True, text=True)
            if build.returncode == 0:
                failure_text = ""
                break
            failure_text = build.stderr
        if failure_text:
            if ("pqxx/pqxx" in failure_text or "-lpqxx" in failure_text
                    or "cannot find -lpq" in failure_text):
                self.skipTest("нечем собрать: нет libpqxx")
            self.fail("проба не собралась:\n%s" % failure_text)

    def _run(self, args, env):
        full = dict(os.environ)
        for key in ("STAND_VARIANT", "STAND_WORKERS"):
            full.pop(key, None)
        full.update(env)
        return subprocess.run([self.binary] + args, env=full,
                              capture_output=True, text=True)

    def test_dispatch_answers_the_way_the_axes_expect(self):
        run = self._run(["dispatch"], {"STAND_VARIANT": "probe-alpha"})
        self.assertEqual(run.returncode, 0,
                         "разбор запроса не сошёлся:\n%s" % run.stderr)
        self.assertIn("PROBE OK", run.stdout)

    def test_worker_count_is_read_as_given(self):
        run = self._run(["workers", "7"],
                        {"STAND_VARIANT": "probe-alpha",
                         "STAND_WORKERS": "7"})
        self.assertEqual(run.returncode, 0,
                         "число рабочих единиц прочитано не как задано:\n%s"
                         % run.stderr)

    def test_unusable_worker_count_refuses_instead_of_defaulting(self):
        # Ноль, отрицательное, нечисло, число с хвостом и пустое значение —
        # всё это НЕ величина. Подставленное вместо них запасное значение
        # дало бы прогон, который выглядит настроенным, а мерит дефолт.
        for value in (None, "", "   ", "0", "-1", "abc", "4x", "4.5"):
            env = {"STAND_VARIANT": "probe-alpha"}
            if value is not None:
                env["STAND_WORKERS"] = value
            run = self._run(["workers-refuse"], env)
            self.assertEqual(
                run.returncode, 0,
                "STAND_WORKERS=%r принято как число рабочих единиц:\n%s"
                % (value, run.stderr))


if __name__ == "__main__":
    unittest.main()
