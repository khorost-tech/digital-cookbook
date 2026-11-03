"""Проверки осей стенда performance/load-testing."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze", os.path.join(HERE, "scripts", "analyze.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

OPEN = ("wrk2", "vegeta", "oha-corrected", "k6-open", "gatling-open")
CLOSED_HIDING = ("hey", "k6-closed", "gatling-closed", "locust-closed", "locust-throughput")


def by(axis):
    return {r["variant"]: r for r in analyze.read(axis)}


class Controls(unittest.TestCase):
    def test_controls(self):
        for a in analyze.AXES:
            analyze.check(a, analyze.read(a))

    def test_broken_model_control_is_refused(self):
        rows = [dict(r, outcome="mismatch") if r["variant"] == "control-vegeta" else r for r in analyze.read("model")]
        with self.assertRaises(SystemExit):
            analyze.check("model", rows)


class Model(unittest.TestCase):
    def test_open_generators_match_truth(self):
        d = by("model")
        for v in OPEN:
            self.assertEqual(d[v]["outcome"], "matches", v)

    def test_closed_generators_hide_the_pause(self):
        # p99 — как без пауз, а максимум — целая пауза: хвост виден только в max
        d = by("model")
        for v in CLOSED_HIDING:
            self.assertTrue(d[v]["p99_hidden"], v)
            self.assertGreaterEqual(d[v]["max_ms~"], 900, v)

    def test_wrk_restores_tail_not_middle(self):
        d = by("model")["wrk"]
        self.assertEqual(d["model"], "closed")
        self.assertEqual(d["outcome"], "matches")
        self.assertTrue(d["p90_hidden"])

    def test_oha_correction_flag(self):
        d = by("model")
        self.assertTrue(d["oha"]["p99_hidden"])
        self.assertFalse(d["oha-corrected"]["p99_hidden"])

    def test_k6_without_vus_drops_requests(self):
        d = by("model")
        self.assertTrue(d["k6-open-capped"]["dropped_any"])
        self.assertTrue(d["k6-open-capped"]["p90_hidden"])
        self.assertFalse(d["k6-open"]["dropped_any"])

    def test_counts_match_server(self):
        for v, r in by("model").items():
            self.assertTrue(r["count_diff_within"], v)


class Types(unittest.TestCase):
    def test_load_meets_slo(self):
        self.assertTrue(by("types")["load"]["slo_met"])

    def test_stress_capacity_below_nominal(self):
        d = by("types")["stress"]
        self.assertTrue(d["capacity_within"])
        self.assertLess(d["capacity_measured~"], d["capacity_truth"])
        # фактическую ёмкость предсказывает занятость обработчика, а не номинал S
        self.assertTrue(d["capacity_matches_busy"])

    def test_spike_recovery_matches_effective_truth(self):
        d = by("types")["spike"]
        self.assertTrue(d["recovery_within"])
        self.assertGreater(d["recovery_measured_s~"], d["recovery_truth_nominal_s"])

    def test_spike_without_vus_is_not_a_spike(self):
        d = by("types")["spike-few-vus"]
        self.assertEqual(d["outcome"], "match")
        self.assertLess(d["spike_rate_server~"], 1000)

    def test_soak_catches_what_short_test_misses(self):
        d = by("types")
        self.assertFalse(d["soak-short"]["oom_killed"])
        self.assertTrue(d["soak-short"]["slo_met"])
        self.assertTrue(d["soak"]["oom_killed"])
        self.assertFalse(d["soak"]["slo_met"])
        self.assertEqual(d["soak"]["outcome"], "match")


class Ceiling(unittest.TestCase):
    def test_native_generators_hold_16000(self):
        d = by("ceiling")
        for t in ("wrk2", "vegeta", "oha-corrected", "k6-open"):
            self.assertTrue(d["%s@16000" % t]["holds"], t)

    def test_gatling_hits_its_ceiling_first(self):
        d = by("ceiling")
        self.assertTrue(d["gatling-open@8000"]["holds"])
        self.assertFalse(d["gatling-open@16000"]["holds"])

    def test_service_is_not_the_limit(self):
        # закрытый wrk выжимает из сервиса на порядок больше верхнего заданного темпа
        self.assertGreater(by("ceiling")["wrk@max"]["achieved_rate~"], 4 * 16000)

    def test_generator_cpu(self):
        d = by("ceiling")
        self.assertGreaterEqual(d["gatling-open@16000+cpu"]["cpu_pct_median_load~"], 190)
        self.assertTrue(90 <= d["locust-closed@max+cpu"]["cpu_pct_median_load~"] <= 110)
        self.assertGreaterEqual(d["locust-closed-2p@max+cpu"]["cpu_pct_median_load~"], 180)
        self.assertLess(d["wrk2@16000+cpu"]["cpu_pct_median_load~"], 100)

    def test_locust_scales_with_processes(self):
        d = by("ceiling")
        one, two = d["locust-closed@max"]["achieved_rate~"], d["locust-closed-2p@max"]["achieved_rate~"]
        self.assertGreater(two, 1.5 * one)
        self.assertLess(one, d["hey@max"]["achieved_rate~"] / 4)


class Chaos(unittest.TestCase):
    def test_latency_breaks_naive_by_time_not_errors(self):
        d = by("chaos")
        n, r = d["latency-naive"], d["latency-resilient"]
        self.assertTrue(n["errors_ok"])
        self.assertFalse(n["latency_ok"])
        self.assertTrue(r["hypothesis_held"])
        self.assertTrue(r["fallback_in_range"])

    def test_dependency_down(self):
        d = by("chaos")
        self.assertFalse(d["down-naive"]["hypothesis_held"])
        self.assertTrue(d["down-naive"]["errors_in_range"])
        # устойчивый держит гипотезу ценой запасных ответов на всё окно сбоя
        self.assertTrue(d["down-resilient"]["hypothesis_held"])
        self.assertTrue(d["down-resilient"]["fallback_in_range"])

    def test_abort_limits_blast_radius(self):
        d = by("chaos")
        self.assertTrue(d["down-naive-abort"]["aborted"])
        self.assertLess(d["down-naive-abort"]["errors~"], d["down-naive"]["errors~"] / 4)

    def test_fault_window(self):
        d = by("chaos")
        for v in ("latency-naive", "down-naive"):
            self.assertFalse(d[v]["held_in_fault_window"], v)
        for v in ("latency-resilient", "down-resilient"):
            self.assertTrue(d[v]["held_in_fault_window"], v)
            self.assertTrue(d[v]["fallback_age_in_range"], v)

    def test_client_retries_hide_replica_loss(self):
        # HAProxy не перенаправляет (redispatch 0) — запрос повторяет сам клиент
        d = by("chaos")
        k, n = d["kill-replica"], d["kill-replica-noretry"]
        self.assertTrue(k["hypothesis_held"])
        self.assertTrue(k["no_redispatch"])
        self.assertTrue(k["client_resent_some"])
        self.assertLess(k["errors~"], n["errors~"])
        self.assertTrue(n["errors_in_range"])

    def test_http_metric_misses_client_retry(self):
        # http_req_duration не включает попытку, повторённую клиентом; полная операция — включает
        d = by("chaos")
        for v in ("kill-replica", "kill-replica-noretry"):
            self.assertTrue(d[v]["hypothesis_held"], v)
            self.assertFalse(d[v]["held_by_operation"], v)
            self.assertGreater(d[v]["op_p99_ms~"], 900, v)
            self.assertLess(d[v]["p99_ms~"], 50, v)

    def test_worst_second_hides_in_totals(self):
        n = by("chaos")["kill-replica-noretry"]
        self.assertTrue(n["hypothesis_held"])
        self.assertTrue(n["worst_second_over"])

    def test_every_variant_as_expected(self):
        for v, r in by("chaos").items():
            self.assertEqual(r["outcome"], "match", v)


if __name__ == "__main__":
    unittest.main()
