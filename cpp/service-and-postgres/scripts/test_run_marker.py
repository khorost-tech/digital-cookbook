# -*- coding: utf-8 -*-
"""Маркер полного прогона сверяется с состоянием стенда, а не верится.

Маркер, который никто не читает, — это строка времени: он переживёт и
переснятую фикстуру, и выросший набор проверок. Здесь он читается.
"""

import hashlib
import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURES = os.path.join(HERE, "fixtures")
MARKER = os.path.join(FIXTURES, "RUN-COMPLETE")


def _run_all():
    with io.open(os.path.join(HERE, "bench", "run-all.sh"),
                 encoding="utf-8") as handle:
        return handle.read()


def _module():
    path = os.path.join(HERE, "scripts", "run-marker.py")
    spec = importlib.util.spec_from_file_location("run_marker_module", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class MarkerDescribesTheStand(unittest.TestCase):

    def setUp(self):
        self.module = _module()
        self.assertTrue(os.path.exists(MARKER),
                        "маркера полного прогона нет: %s — прогон не "
                        "объявлялся полным" % MARKER)
        with io.open(MARKER, encoding="utf-8") as handle:
            self.marker = json.loads(handle.read())

    def test_every_axis_fixture_is_listed(self):
        """Все пять фикстур, и ни одной лишней."""
        self.assertEqual(sorted(self.marker["fixtures_sha256"]),
                         sorted(self.module.FIXTURE_FILES))

    def test_sums_match_the_fixtures_on_disk(self):
        """Правленная фикстура краснит маркер, а не проходит молча."""
        for name, written in sorted(self.marker["fixtures_sha256"].items()):
            with io.open(os.path.join(FIXTURES, name), "rb") as handle:
                actual = hashlib.sha256(handle.read()).hexdigest()
            self.assertEqual(
                written, actual,
                "сумма %s в маркере не сходится с файлом: фикстура правлена "
                "или переснята после того, как прогон объявлен полным"
                % name)

    def test_check_count_matches_the_suite(self):
        """Выросший набор проверок краснит маркер."""
        self.assertEqual(self.marker["checks"], self.module.check_count(),
                         "число проверок в маркере не сходится с набором: "
                         "маркер снят до того, как набор изменился")

    def test_sabotage_count_matches_the_registry(self):
        self.assertEqual(self.marker["sabotages"],
                         self.module.sabotage_count(),
                         "число диверсий в маркере не сходится с реестром")

    def test_marker_is_the_last_step_of_the_run(self):
        """Маркер ставится ПОСЛЕ проверок, а не до них.

        Маркер, поставленный раньше набора проверок, описывает состояние,
        которое ещё никто не проверял: прогон, упавший на проверках,
        оставил бы после себя полноценный на вид маркер.
        """
        steps = [line for line in _run_all().splitlines()
                 if line.startswith("step ")]
        self.assertTrue(steps, "в bench/run-all.sh нет ни одного шага")
        self.assertIn("run-marker.py", steps[-1],
                      "последний шаг прогона — не маркер: %s" % steps[-1])
        checks = [i for i, line in enumerate(steps) if "unittest" in line]
        self.assertTrue(checks, "в прогоне нет шага с набором проверок")
        self.assertLess(checks[-1], len(steps) - 1,
                        "набор проверок идёт ПОСЛЕ маркера")

    def test_sums_are_sums_and_not_placeholders(self):
        """Сумма обязана быть суммой: 64 шестнадцатеричных знака."""
        for name, written in sorted(self.marker["fixtures_sha256"].items()):
            self.assertRegex(written, r"^[0-9a-f]{64}$",
                             "в маркере у %s не сумма sha256" % name)


class RunAllIsHonest(unittest.TestCase):
    """Сводный прогон: код возврата и порядок шагов.

    Проверка ТЕКСТОВАЯ, и это её предел: она видит, что код возврата
    снимается и что маркер снимается первым делом, но не гоняет сам
    прогон — он идёт полчаса и требует докера. Зато дефект, который она
    ловит, — молчаливый: прогон, глотающий код возврата шага, выглядит
    успешным ровно так же, как прошедший.
    """

    def setUp(self):
        self.text = _run_all()

    def test_every_axis_is_run(self):
        for name in ("run-variants.py", "run-db-wait.py", "run-pool.py",
                     "run-prepared.py", "run-cancel.py"):
            self.assertIn(name, self.text,
                          "сводный прогон не зовёт %s — фикстура осталась "
                          "бы от прошлого прогона, а маркер объявил бы "
                          "прогон полным" % name)

    def test_the_step_return_code_is_checked(self):
        """Шаг, упавший молча, делает весь прогон ложью."""
        self.assertIn("rc=$?", self.text,
                      "код возврата шага не снимается")
        self.assertIn("exit $rc", self.text,
                      "снятый код возврата никуда не ведёт: прогон "
                      "продолжится после упавшего шага")

    def test_the_marker_is_removed_before_the_run(self):
        """Пока прогон идёт, маркера нет — и это видно."""
        self.assertIn('rm -f "$MARKER"', self.text,
                      "маркер прошлого прогона не снимается: оборванный "
                      "прогон оставил бы его на месте")
        before = self.text.index('rm -f "$MARKER"')
        after = self.text.index("run-marker.py")
        self.assertLess(before, after,
                        "маркер снимается уже после того, как поставлен")
