"""Проверки осей 3 и 8 (задача 4)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_errors", os.path.join(HERE, "scripts", "analyze-errors.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Errors(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_details_reach_every_client(self):
        for r in self.rows:
            if r["kind"] == "errors" and r["mode"] == "details":
                self.assertEqual((r["code"], r["details_reason"]), ("NotFound", "RECORD_NOT_FOUND"), r["variant"])

    def test_gateway_details_need_registered_types(self):
        g = {(r["server"], r["mode"]): r for r in self.rows if r["kind"] == "gateway"}
        for srv in ("go", "java"):
            self.assertEqual(g[(srv, "details")]["http_status"], 500)
            self.assertEqual(g[(srv, "details-registered")]["http_status"], 404)
            self.assertEqual(g[(srv, "details-registered")]["body_reason"], "RECORD_NOT_FOUND")

    def test_unrecovered_go_panic_that_survives_is_refused(self):
        row = next(r for r in self.rows if r["kind"] == "errors" and r["mode"] == "panic" and r["server"] == "go")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, container_running=True, health_after=True)])


if __name__ == "__main__":
    unittest.main()
