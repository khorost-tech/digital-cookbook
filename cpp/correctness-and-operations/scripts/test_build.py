"""Проверки задачи 2: внесённые ошибки — это флаг сборки, а не другой код.

Живые проверки требуют докера и включаются STAND_WITH_DOCKER=1, как у стенда
волны B: штатный прогон без докера их пропускает, и это видно в выводе.
"""

import os
import unittest

from scripts import docker
from scripts.build import build_script

LIVE = os.environ.get("STAND_WITH_DOCKER") == "1"


def symbols(planted):
    # Только СОБСТВЕННЫЕ функции библиотеки (сильные символы `T` в parse::).
    # Слабые символы — инстанциации шаблонов STL — на -O0 зависят от того,
    # какой код их использует, и различаются законно: первая редакция этой
    # проверки сравнивала и их и падала на std::vector::back() из P2.
    script = build_script(planted, build_dir="/tmp/b") + (
        " && nm -C --defined-only /tmp/b/libparse.a | awk '$2 == \"T\"' | cut -d' ' -f3- | grep '^parse::' | sort -u")
    rc, out, _ = docker.run_in_base(script)
    if rc != 0:
        raise RuntimeError(out)
    return set(out.splitlines())


def plain_tests(planted):
    script = build_script(planted, build_dir="/tmp/b") + " && /tmp/b/parse_test --gtest_brief=1"
    return docker.run_in_base(script)


@unittest.skipUnless(LIVE, "живая проверка: STAND_WITH_DOCKER=1")
class PlantedIsAFlag(unittest.TestCase):
    def test_same_symbols(self):
        # Ошибки P1–P6 живут ВНУТРИ функций. Если вариант с ними даёт другой
        # набор символов, ось 9.1 сравнивала бы разный код, а не флаг.
        a, b = symbols(False), symbols(True)
        self.assertTrue(a, "пустой набор символов — сборка не та")
        self.assertEqual(a, b)

    def test_plain_build_green_both_ways(self):
        # Предмет оси 9.1: без санитайзера внесённые ошибки не видны.
        for planted in (False, True):
            rc, out, _ = plain_tests(planted)
            self.assertEqual(rc, 0, "PLANTED=%s:\n%s" % (planted, out[-2000:]))
            self.assertIn("PASSED", out)


if __name__ == "__main__":
    unittest.main()
