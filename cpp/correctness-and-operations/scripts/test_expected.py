"""Проверки expected.json: все оси спеки на месте, известное отделено от гипотез."""

import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ["bench", "config", "coverage", "defects", "fds", "logs", "shutdown"]


def load():
    with open(os.path.join(HERE, "expected.json"), encoding="utf-8") as f:
        return json.load(f)


def axes():
    return {k: v for k, v in load().items() if isinstance(v, dict)}


class Expected(unittest.TestCase):
    def test_every_axis_present(self):
        self.assertEqual(sorted(axes()), AXES)

    def test_every_axis_has_hypothesis_and_control(self):
        for name, axis in axes().items():
            self.assertTrue(axis.get("hypothesis_before_run"), name)
            self.assertTrue(axis.get("control"), name)
            self.assertTrue(axis.get("confidence"), name)

    def test_known_facts_carry_source(self):
        for name, axis in axes().items():
            for fact in axis["known_from_pre_run_probes"]:
                self.assertTrue(fact.get("fact") and fact.get("source"), name)

    def test_hypothesis_does_not_cite_probes(self):
        # Гипотеза, ссылающаяся на пробу, — это известное, выданное за
        # предсказание. Место ему в known_from_pre_run_probes.
        for name, axis in axes().items():
            text = axis["hypothesis_before_run"]
            for marker in ("probe.jsonl", "проба", "черновой прогон", "замерено"):
                self.assertNotIn(marker, text, name)


if __name__ == "__main__":
    unittest.main()
