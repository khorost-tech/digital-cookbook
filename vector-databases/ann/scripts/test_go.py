"""Тесты Go-пакетов (hnsw, ivf, quant) — в контейнере Go по дайджесту.

Нужен Docker; при STAND_NO_DOCKER=1 (реестр диверсий) пропускается: диверсии
бьют по фикстурам, а не по коду.
"""

import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402


@unittest.skipIf(os.environ.get("STAND_NO_DOCKER") == "1", "без Docker")
class GoTests(unittest.TestCase):
    def test_go_packages(self):
        p = subprocess.run(["docker", "run", "--rm", "-v", os.path.join(stack.HERE, "go") + ":/src",
                            "-v", "vdb-gocache:/root/.cache/go-build", "-w", "/src", stack.image("golang"),
                            "go", "test", "-count=1", "./..."], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        self.assertEqual(p.returncode, 0, p.stdout[-3000:])


if __name__ == "__main__":
    unittest.main()
