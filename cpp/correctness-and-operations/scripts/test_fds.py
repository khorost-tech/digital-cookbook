"""Проверки оси 5 (задача 5.1, изменения 05.10)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_fds", os.path.join(HERE, "scripts", "analyze-fds.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Fds(unittest.TestCase):
    def test_control_no_growth_without_errors(self):
        rows = analyze.read()
        for own in ("raw", "raii"):
            self.assertEqual(analyze.row(rows, own, 0)["delta"], 0, own)

    def test_counted_from_proc_not_from_service(self):
        for r in analyze.read():
            self.assertEqual(r["source"], "/proc/1/fd")

    def test_error_requests_match_m(self):
        analyze.check(analyze.read())

    def test_wrong_response_count_is_refused(self):
        # Контрольные строки на месте и в порядке — падать обязана только
        # сверка ответов с M. Первая редакция без них падала на отсутствии
        # контроля и не замечала диверсию, снимавшую сверку.
        def r(own, fail, responses, delta):
            return {"variant": own, "ownership": own, "fail": fail, "m": 100,
                    "source": "/proc/1/fd", "responses": responses, "delta": delta}
        good = [r("raw", 0, {"200": 100}, 0), r("raii", 0, {"200": 100}, 0),
                r("raii", 1, {"500": 100}, 0)]
        analyze.check(good + [r("raw", 1, {"500": 100}, 100)])
        with self.assertRaises(SystemExit):
            analyze.check(good + [r("raw", 1, {"500": 99, "200": 1}, 99)])


if __name__ == "__main__":
    unittest.main()
