"""Доля от своего пола считается ПОКРУГОВО, а не отношением медиан.

Это не вкусовая деталь. Фреймворк и его контроль меряются в одном круге,
поэтому общее состояние машины из парного отношения частично сокращается, а
из отношения медиан — нет. Разница на этом стенде доходила до двух
процентных пунктов, и пока анализатор печатал одно, а статья другое,
headline-таблица не воспроизводилась публичной командой.

Тест закрепляет и способ счёта, и то, что напечатанное совпадает со статьёй.
"""

import os
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))
os.environ.setdefault("FIXTURE", str(ROOT / "fixtures" / "endpoints.txt"))

import importlib.util  # noqa: E402

spec = importlib.util.spec_from_file_location(
    "analyze_endpoints", ROOT / "scripts" / "analyze-endpoints.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


def row(framework, endpoint, conc, rnd, rps):
    return {"framework": framework, "endpoint": endpoint,
            "concurrency": conc, "round": rnd, "rps": rps}


class TestPairedShares(unittest.TestCase):
    def test_pairs_within_a_round(self):
        data = [
            row("fastapi", "ping", 64, 1, 50), row("raw-asgi", "ping", 64, 1, 100),
            row("fastapi", "ping", 64, 2, 90), row("raw-asgi", "ping", 64, 2, 100),
        ]
        self.assertEqual(mod.paired_shares(data, "fastapi", "raw-asgi",
                                           "ping", 64), [50.0, 90.0])

    def test_differs_from_ratio_of_medians(self):
        # Круг 1 медленный у обоих, круг 2 быстрый. Парное отношение
        # одинаково в обоих кругах — состояние машины сократилось. Отношение
        # медиан дало бы другое число, если бы круги были рассинхронены.
        data = [
            row("fastapi", "ping", 64, 1, 40), row("raw-asgi", "ping", 64, 1, 80),
            row("fastapi", "ping", 64, 2, 60), row("raw-asgi", "ping", 64, 2, 200),
        ]
        shares = mod.paired_shares(data, "fastapi", "raw-asgi", "ping", 64)
        self.assertEqual(shares, [30.0, 50.0])
        self.assertEqual(mod.med(shares), 40.0)
        # Отношение медиан: med(40,60)/med(80,200) = 50/140 = 35,7%.
        ratio_of_medians = 100 * mod.med([40, 60]) / mod.med([80, 200])
        self.assertNotAlmostEqual(mod.med(shares), ratio_of_medians, places=1)

    def test_incomplete_round_is_skipped_not_guessed(self):
        # Круг, где нет пары, в выборку попасть не должен: иначе доля
        # посчиталась бы от чужого круга.
        data = [
            row("fastapi", "ping", 64, 1, 50), row("raw-asgi", "ping", 64, 1, 100),
            row("fastapi", "ping", 64, 2, 90),
        ]
        self.assertEqual(mod.paired_shares(data, "fastapi", "raw-asgi",
                                           "ping", 64), [50.0])

    def test_zero_floor_does_not_divide(self):
        data = [row("fastapi", "ping", 64, 1, 50),
                row("raw-asgi", "ping", 64, 1, 0)]
        self.assertEqual(mod.paired_shares(data, "fastapi", "raw-asgi",
                                           "ping", 64), [])

    def test_matches_what_the_article_publishes(self):
        # Числа статьи берутся отсюда, поэтому сверяем с реальной фикстурой.
        data = mod.rows()
        expected = {("fastapi", "ping"): 74, ("fastapi", "echo"): 70,
                    ("fastapi", "wait"): 102, ("django", "wait"): 27,
                    ("flask", "wait"): 99}
        for (target, ep), want in expected.items():
            floor = "raw-wsgi" if target == "flask" else "raw-asgi"
            got = round(mod.med(mod.paired_shares(data, target, floor, ep, 64)))
            self.assertEqual(got, want, f"{target}/{ep}: {got}% против {want}%")


if __name__ == "__main__":
    unittest.main(verbosity=2)
