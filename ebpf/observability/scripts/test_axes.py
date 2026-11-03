"""Проверки осей стенда ebpf/observability."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze", os.path.join(HERE, "scripts", "analyze.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


def by(axis):
    return {r["variant"]: r for r in analyze.read(axis)}


class Controls(unittest.TestCase):
    def test_controls(self):
        for a in analyze.AXES:
            analyze.check(a, analyze.read(a))

    def test_broken_sdk_control_is_refused(self):
        rows = [dict(r, mismatched_routes=["GET /fail"]) if r["variant"] == "sdk-routes" else r
                for r in analyze.read("traces")]
        with self.assertRaises(SystemExit):
            analyze.check("traces", rows)


class Traces(unittest.TestCase):
    def test_beyla_counts_like_sdk(self):
        d = by("traces")
        self.assertEqual(d["beyla-routes"]["mismatched_routes"], [])
        self.assertEqual(d["beyla-sql"]["got_select_insert"], d["beyla-sql"]["want"])

    def test_route_names(self):
        d = by("traces")["beyla-route-names"]
        self.assertIn("/users/{id}", d["routes_8080"])
        self.assertEqual(d["routes_8081"], ["/v2/users/*"])
        self.assertGreater(d["distinct_paths_8081"], 1)

    def test_links(self):
        d = by("traces")
        b, s = d["beyla-links"], d["sdk-links"]
        self.assertEqual((b["same_trace"], b["parent_found"]), (b["sql_spans"], 0))
        self.assertEqual(s["parent_found"], s["sql_spans"])

    def test_startup_query(self):
        # SDK видит CREATE TABLE при старте сервиса, Beyla прицепилась позже
        d = by("traces")
        self.assertTrue(d["sdk-sql"]["startup_create_seen"])
        self.assertFalse(d["beyla-sql"]["startup_create_seen"])
        self.assertEqual(d["sdk-sql"]["other_extra"], [])


class Profiles(unittest.TestCase):
    def test_symbolization(self):
        d = by("profiles")
        for v in ("go126", "go126-stripped", "go127"):
            self.assertTrue(d[v]["within_tolerance"], v)
        self.assertTrue(d["go127"]["unsupported_go"])
        self.assertFalse(d["go127-stripped"]["within_tolerance"])
        self.assertEqual(d["go127-stripped"]["burn_functions"], [])
        self.assertEqual(d["go126"]["burn_functions"], ["main.burnSelfA", "main.burnSelfB"])

    def test_leaf_hides_caller(self):
        d = by("profiles")["go126-shared"]
        self.assertFalse(d["within_tolerance"])
        self.assertEqual(d["top_self"], "main.work")
        # контроль: тот же режим, раскрутка рантаймом Go — вызывающие на месте
        p = by("profiles")["pprof-shared"]
        self.assertTrue(p["within_tolerance"])
        self.assertTrue(p["go"].startswith("go1.26"))  # тот же тулчейн, что у go126-shared


class Cost(unittest.TestCase):
    def test_programs_fire_only_under_load(self):
        d = by("cost")
        self.assertTrue(d["idle"]["background_below_1pct"])
        self.assertGreater(d["load"]["run_cnt_per_request~"], 1)


if __name__ == "__main__":
    unittest.main()
