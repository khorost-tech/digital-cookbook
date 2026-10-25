"""Проверки оси 5 (задача 6)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze_retry", os.path.join(HERE, "scripts", "analyze-retry.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Retry(unittest.TestCase):
    def setUp(self):
        self.rows = analyze.read()

    def test_controls(self):
        analyze.check(self.rows)

    def test_retryable_codes_only(self):
        for r in self.rows:
            if r["name"] == "internal":
                self.assertEqual(r["server_attempts"], 1, r["variant"])
            if r["name"] == "unavailable":
                self.assertEqual((r["server_attempts"], r["client_code"]), (3, "OK"), r["variant"])

    def test_committed_stream_is_not_retried(self):
        rows = {(r["client"], r["server"], r["name"]): r for r in self.rows if r["kind"] == "stream-retry"}
        for c in ("go", "java"):
            for s in ("go", "java"):
                self.assertEqual(rows[(c, s, "stream-no-policy")]["server_attempts"], 1)
                self.assertEqual(rows[(c, s, "stream-before")]["server_attempts"], 4)
                # заголовки ответа уже фиксируют вызов — сообщений ещё нет
                headers = rows[(c, s, "stream-headers")]
                self.assertEqual((headers["server_attempts"], headers["received"]), (1, 0))
                after = rows[(c, s, "stream-after")]
                self.assertEqual((after["server_attempts"], after["received"]), (1, 1))

    def test_deadline_variant_is_deterministic(self):
        for r in self.rows:
            if r.get("name") == "deadline":
                self.assertEqual(r["server_attempts"], 1, r["variant"])

    def test_max_attempts_capped(self):
        for r in self.rows:
            if r["name"] == "max10":
                self.assertEqual(r["server_attempts"], 5, r["variant"])

    def test_retry_by_probe_is_refused(self):
        row = next(r for r in self.rows if r["name"] == "unavailable")
        with self.assertRaises(SystemExit):
            analyze.check([dict(row, success_attempt=1)])


if __name__ == "__main__":
    unittest.main()
