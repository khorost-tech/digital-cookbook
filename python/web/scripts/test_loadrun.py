"""Проверка отбраковки простоя. Предохранитель заводится вместе со случаем,
который его роняет: без этого файла нельзя отличить работающую отбраковку от
той, что молча пропускает всё подряд.
"""

import json
import os
import subprocess
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", "bench"))

import loadrun  # noqa: E402


def result(idle, rps, count=1000):
    return subprocess.CompletedProcess(
        [], 0, stdout=json.dumps({"idle_fraction": idle, "rps": rps,
                                  "count": count}), stderr="")


class TestRunLoad(unittest.TestCase):
    def run_with(self, sequence):
        with mock.patch.object(loadrun.subprocess, "run",
                               side_effect=sequence) as run:
            data, discarded = loadrun.run_load(["x"], ".", {})
        return data, discarded, run.call_count

    def test_healthy_round_passes_without_retry(self):
        data, discarded, calls = self.run_with([result(0.004, 2800)])
        self.assertEqual(calls, 1)
        self.assertEqual(discarded, [])
        self.assertEqual(data["rps"], 2800)

    def test_idle_round_is_retried_and_recorded(self):
        data, discarded, calls = self.run_with(
            [result(0.41, 1660), result(0.004, 2800)])
        self.assertEqual(calls, 2)
        # Отброшенный круг обязан остаться в строке фикстуры.
        self.assertEqual(discarded, [0.41])
        self.assertEqual(data["idle_discarded"], [0.41])
        self.assertEqual(data["rps"], 2800)

    def test_persistent_idle_returns_last_and_stays_visible(self):
        data, _, calls = self.run_with([result(0.41, 1660)] * loadrun.ATTEMPTS)
        self.assertEqual(calls, loadrun.ATTEMPTS)
        # Не молчаливый успех: клетка возвращается с высоким простоем,
        # чтобы анализатор её отверг.
        self.assertGreater(data["idle_fraction"], loadrun.MAX_IDLE)

    def test_zero_requests_is_a_failure_not_a_zero_rate(self):
        # Нагрузчик, у которого не получилось ни одного запроса, печатает
        # правдоподобную строку: rps=0, простой 0, ошибок «нет». Отличить
        # её от честного нуля можно только по числу запросов.
        data, _, _ = self.run_with([result(0.0, 0.0, count=0)])
        self.assertIsNone(data)

    def test_empty_output_is_a_failure_not_a_zero(self):
        with mock.patch.object(loadrun.subprocess, "run",
                               return_value=subprocess.CompletedProcess(
                                   [], 1, stdout="", stderr="boom")):
            data, _ = loadrun.run_load(["x"], ".", {})
        self.assertIsNone(data)


if __name__ == "__main__":
    unittest.main(verbosity=2)
