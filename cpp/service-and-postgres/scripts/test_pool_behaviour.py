"""Поведение пула соединений — исполнением, а не чтением текста.

ЗАЧЕМ ОТДЕЛЬНО ОТ ПРОВЕРОК ОСИ. Ось 2 считает соединения ИЗ БАЗЫ и
отвечает на вопрос «сколько их открыто». Здесь предмет другой: правила
выдачи. Слот, не вернувшийся при выходе исключением, не портит тот замер,
в котором утёк, — соединения копятся между залпами, и остаток
предыдущего прогона следующий читает как свойство слоя. Из базы это
видно как странное число, а не как утечка.

ЧТО ПРОГОНЯЕТСЯ. `service/pool_probe.cpp` — заголовок пула с подставным
«соединением» вместо pqxx. База не нужна: правила выдачи от неё не
зависят.

ЕСЛИ СОБРАТЬ НЕЧЕМ. Проверка объявляет себя ПРОПУЩЕННОЙ с причиной, а не
пройденной: зелёный результат на машине без компилятора означал бы, что
поведение проверено, — а оно не проверялось.
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


class PoolBehaviour(unittest.TestCase):
    def test_probe_runs_and_all_assertions_hold(self):
        compiler = _compiler()
        if compiler is None:
            self.skipTest("нечем собрать: компилятор C++ не найден")
        work = tempfile.mkdtemp(prefix="pool-probe-")
        binary = os.path.join(work, "probe")
        try:
            # Ни libpqxx, ни базы: заголовок пула их не включает
            # намеренно — иначе эта проверка шла бы только там, где есть
            # PostgreSQL, то есть почти нигде.
            build = subprocess.run(
                [compiler, "-std=c++20", "-I", SERVICE,
                 os.path.join(SERVICE, "pool_probe.cpp"),
                 "-o", binary, "-pthread"],
                capture_output=True, text=True)
            self.assertEqual(build.returncode, 0,
                             "проба пула не собралась:\n%s" % build.stderr)
            run = subprocess.run([binary], capture_output=True, text=True)
            self.assertEqual(
                run.returncode, 0,
                "проба пула не сошлась:\n%s" % run.stderr)
            self.assertIn("PROBE OK", run.stdout)
        finally:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
