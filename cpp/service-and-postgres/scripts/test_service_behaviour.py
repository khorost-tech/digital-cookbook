"""Проверка поведения общей логики — исполнением, а не чтением текста.

ЗАЧЕМ ОНА ПОЯВИЛАСЬ. Проверки исходника держат форму, и дважды этого не
хватило: сверка имени варианта пережила замену условия на тавтологию,
классификация отказа — дописанное `|| true`. В обоих случаях текст, за
который цеплялась проверка, остался на месте, а решение перестало
зависеть от входа.

ЧТО ЗДЕСЬ ПРОГОНЯЕТСЯ. `service/behaviour_probe.cpp` собирается вместе с
`service/main.cpp` и запускается. База не нужна: оба решения принимаются
до похода в неё.

ЕСЛИ СОБРАТЬ НЕЧЕМ. Проверка объявляет себя ПРОПУЩЕННОЙ с причиной, а не
пройденной: на машине без компилятора или без заголовков libpqxx зелёный
результат означал бы, что поведение проверено, — а оно не проверялось.
"""

import os
import shutil
import subprocess
import tempfile
import unittest

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SERVICE = os.path.join(STAND, "service")


def _compiler():
    for name in ("g++", "c++", "clang++"):
        found = shutil.which(name)
        if found:
            return found
    return None


class Behaviour(unittest.TestCase):
    def test_probe_runs_and_all_assertions_hold(self):
        compiler = _compiler()
        if compiler is None:
            self.skipTest("нечем собрать: компилятор C++ не найден")
        work = tempfile.mkdtemp(prefix="behaviour-probe-")
        binary = os.path.join(work, "probe")
        try:
            # ПОЧЕМУ СТАНДАРТ ПОДБИРАЕТСЯ, А НЕ ЗАДАН. Стенд собирается под
            # C++20, но libpqxx, установленная в систему, собрана своим
            # стандартом, и сигнатуры в ней отличаются: при несовпадении
            # проба не компилируется, а ЛИНКУЕТСЯ с ошибкой про
            # std::source_location. Это свойство машины, на которой идёт
            # разбор, а не дефект стенда, и падать из-за него проверка
            # поведения не должна. В образе оба стандарта совпадают.
            failure_text = ""
            for standard in ("c++20", "c++17"):
                build = subprocess.run(
                    [compiler, "-std=" + standard, "-I", SERVICE,
                     os.path.join(SERVICE, "main.cpp"),
                     os.path.join(SERVICE, "behaviour_probe.cpp"),
                     "-o", binary, "-lpqxx", "-lpq", "-pthread"],
                    capture_output=True, text=True)
                if build.returncode == 0:
                    failure_text = ""
                    break
                failure_text = build.stderr
            if failure_text:
                missing = ("pqxx/pqxx" in failure_text
                           or "-lpqxx" in failure_text
                           or "cannot find -lpq" in failure_text)
                if missing:
                    self.skipTest("нечем собрать: нет libpqxx")
                self.fail("проба не собралась:\n%s" % failure_text)
            run = subprocess.run([binary], capture_output=True, text=True)
            self.assertEqual(
                run.returncode, 0,
                "проба поведения не сошлась:\n%s" % run.stderr)
            self.assertIn("PROBE OK", run.stdout)
        finally:
            shutil.rmtree(work, ignore_errors=True)
