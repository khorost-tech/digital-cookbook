"""Проверки осей стенда ebpf/fundamentals."""

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

    def test_broken_hook_control_is_refused(self):
        rows = [dict(r, counted=r["n"] - 1) if r["variant"] == "tracepoint" else r for r in analyze.read("hooks")]
        with self.assertRaises(SystemExit):
            analyze.check("hooks", rows)

    def test_ring_accounting_is_checked(self):
        rows = [dict(r, accounted=False) if r["variant"] == "ring-small" else r for r in analyze.read("maps")]
        with self.assertRaises(SystemExit):
            analyze.check("maps", rows)


class Hooks(unittest.TestCase):
    def test_kernel_hooks_count_exactly(self):
        d = by("hooks")
        for v in ("tracepoint", "kprobe", "fentry", "uprobe", "uretprobe"):
            self.assertEqual(d[v]["counted"], d[v]["n"], v)

    def test_go_stack_growth_traps(self):
        d = by("hooks")
        self.assertEqual(d["uprobe-grow"]["per_call"], 2)  # пролог выполняется дважды
        g = d["uretprobe-grow"]
        self.assertNotEqual(g["load_exit"], "ok")
        self.assertIn("unexpected return pc", g["load_crash"])


class Maps(unittest.TestCase):
    def test_race_and_fixes(self):
        d = by("maps")
        self.assertTrue(d["hash-racy"]["lost"])
        for v in ("hash-atomic", "percpu", "ring-big"):
            self.assertEqual(d[v]["counted"], d[v]["n"], v)

    def test_small_ring_drops(self):
        d = by("maps")
        self.assertTrue(d["ring-small"]["lost"] and d["ring-read"]["lost"])
        self.assertLess(d["ring-small"]["counted"], 4096 // 16)  # заголовок 8 байт + 8 байт события


class Verifier(unittest.TestCase):
    def test_verdicts(self):
        d = by("verifier")
        for v in ("ok", "loop_bounded", "bpf_loop", "packet_checked"):
            self.assertEqual(d[v]["stage"], "accepted", v)
        for v in ("null_deref_hash", "loop_unbounded", "packet_oob", "gpl_helper"):
            self.assertEqual(d[v]["stage"], "verifier", v)
        self.assertEqual(d["stack_big"]["stage"], "clang")

    def test_bpf_loop_runs_all_iterations(self):
        # загрузка и исполнение — разные утверждения: проверено и второе
        b = by("verifier")["bpf_loop"]
        self.assertTrue(b["run_complete"])
        self.assertEqual(b["run_iterations"], b["run_n"])

    def test_surprises_recorded(self):
        # гипотеза ждала отказа, программа принята — расхождение записано в expected.json
        d = by("verifier")
        for v in ("null_deref", "loop_collapsed", "stack_uninit"):
            self.assertEqual((d[v]["stage"], d[v]["outcome"]), ("accepted", "mismatch"), v)


class Core(unittest.TestCase):
    def test_relocations(self):
        d = by("core")
        self.assertEqual((d["guarded"]["branch"], d["guarded"]["value_matches"]), ("__state", True))
        self.assertEqual(d["old"]["stage"], "load")
        self.assertIn("bad CO-RE relocation", d["old"]["reason"])


class Bpftrace(unittest.TestCase):
    def test_exact(self):
        for v, r in by("bpftrace").items():
            self.assertEqual(r["counted"], r["n"], v)


if __name__ == "__main__":
    unittest.main()
