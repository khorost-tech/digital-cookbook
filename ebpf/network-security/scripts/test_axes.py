"""Проверки осей стенда ebpf/network-security."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("analyze", os.path.join(HERE, "scripts", "analyze.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

BLOCK = ("10.99.0.2", "10.99.0.3")


def by(axis):
    return {r["variant"]: r for r in analyze.read(axis)}


class Controls(unittest.TestCase):
    def test_controls(self):
        for a in analyze.AXES:
            analyze.check(a, analyze.read(a))

    def test_broken_filter_control_is_refused(self):
        rows = [dict(r, outcome="mismatch") if r["variant"] == "none" else r for r in analyze.read("filter")]
        with self.assertRaises(SystemExit):
            analyze.check("filter", rows)


class Filter(unittest.TestCase):
    def test_all_filters_block_exactly(self):
        for v in ("xdp-native", "xdp-generic", "tc", "iptables", "xdp-update"):
            r = by("filter")[v]
            self.assertEqual([r["received"][ip] for ip in BLOCK], [0, 0], v)
            self.assertEqual(r["received"]["10.99.0.1"], r["sent"]["10.99.0.1"], v)

    def test_tcpdump_sees_only_after_xdp(self):
        d = by("filter")
        for v in ("xdp-native", "xdp-generic", "xdp-update"):
            self.assertEqual(d[v]["tcpdump_blocked"], 0, v)
        for v in ("tc", "iptables", "none"):
            self.assertEqual(d[v]["tcpdump_blocked"], 20000, v)

    def test_program_counters(self):
        d = by("filter")
        for v in ("xdp-native", "xdp-generic", "tc"):
            self.assertEqual((d[v]["prog_dropped"], d[v]["prog_passed"]), (20000, 20000), v)
        self.assertEqual(d["iptables"]["iptables_dropped"], 20000)

    def test_live_update(self):
        u = by("filter")["xdp-update"]
        self.assertEqual(sum(u["before_update_received"].values()), 40000)


class Cilium(unittest.TestCase):
    def test_lb_map_matches(self):
        c = by("cilium")["cilium"]
        self.assertEqual((c["lb_services"], c["lb_backends"]), (c["services"], c["endpoints"]))
        self.assertEqual((c["map_services"], c["map_backends"]), (c["services"], c["endpoints"]))
        self.assertTrue(c["map_matches_endpointslices"])

    def test_iptables_vs_ebpf(self):
        d = by("cilium")
        self.assertGreater(d["kubeproxy"]["kube_rules"], d["kubeproxy"]["endpoints"])
        self.assertFalse(d["kubeproxy"]["has_ebpf_programs"])
        self.assertTrue(d["cilium"]["has_ebpf_programs"])
        self.assertLess(d["cilium"]["kube_rules"], 5)
        self.assertIn("cil_from_container", d["cilium"]["program_names"])

    def test_socket_lb(self):
        # балансировка при connect/sendmsg: программы Cilium на cgroup обоих узлов
        d = by("cilium")
        self.assertEqual(d["kubeproxy"]["socket_lb_links"], 0)
        self.assertEqual(d["cilium"]["socket_lb_cgroups"], 2)
        self.assertIn("cgroup_inet4_connect", d["cilium"]["socket_lb_attach_types"])
        self.assertIn("cgroup_udp4_sendmsg", d["cilium"]["socket_lb_attach_types"])


if __name__ == "__main__":
    unittest.main()
