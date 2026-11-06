import json, unittest
from pathlib import Path
from contract import Contract
ROOT = Path(__file__).resolve().parents[1]
CONTRACT = Contract(outcomes=("match", "mismatch"), success="match", fields=("kind", "variant", "probe", "outcome", "detail", "cmd", "actual"))
HOSTS, LANGS = {"mac", "devproxy"}, {"c", "go"}


def rows():
    return {r["variant"]: r for r in CONTRACT.read_fixture(ROOT/"fixtures/checks.jsonl")}


def actual(key):
    return rows()[key]["actual"]


def cells(a):
    return [v for h in HOSTS for v in (a[h][l] for l in LANGS)]


class Checks(unittest.TestCase):
    def test_contract_complete(self):
        r = rows()
        hyp = json.loads((ROOT/"expected.json").read_text())
        expected = dict(hyp["checks"])  # набор ключей — исходные проверки и добавленные пересмотрами revision*
        for k in sorted(k for k in hyp if k.startswith("revision")):
            expected.update(hyp[k].get("checks", {}))
        self.assertEqual(sorted(r), sorted(expected))
        for k, v in r.items():
            self.assertEqual(v["cmd"], "python3 scripts/run-checks.py " + k)
            self.assertEqual(v["outcome"], "match")

    def test_control(self):
        a = actual("control")
        self.assertEqual(set(a["checksums_agree_c_go"].values()), {True})       # C и Go считают одно и то же
        self.assertEqual(set(a["other_seed_changes_checksum"].values()), {True}) # контроль сверки: расхождение видно
        self.assertTrue(a["pmu_counts_cycles_privileged"])
        self.assertFalse(a["pmu_counts_cycles_unprivileged"])                    # контроль: без привилегий счётчиков нет
        self.assertEqual(a["line_bytes"], a["go_cache_line_pad"])                # паддинг Go совпадает с линией хоста

    def test_hierarchy(self):
        for row in cells(actual("hierarchy")):
            self.assertTrue(row["grows_to_ram"])
            self.assertTrue(row["l1_plateau_32k_vs_16k"])                        # контроль: внутри L1 ступеньки нет

    def test_aos_soa(self):
        for row in cells(actual("aos_soa")):
            self.assertTrue(row["soa_no_gain_large_all_fields"])                # контроль: нужны все поля — SoA не выигрывает
            self.assertTrue(row["same_small_one_field"])                        # контроль: в кэше раскладка не важна
            self.assertTrue(row["soa_faster_large_one_field"])
            self.assertTrue(row["hot_cold_split_faster_large"])

    def test_false_sharing(self):
        for row in cells(actual("false_sharing")):
            self.assertTrue(row["slower_than_256_at_8"])                         # контроль: одно слово линии — эффект есть

    def test_branch(self):
        for row in cells(actual("branch")):
            self.assertEqual(row["branchless_unsorted_over_sorted"], "<1.5")     # контроль: без ветки порядок данных не важен
            self.assertEqual(row["filter_branchless_unsorted_over_sorted"], "<1.5")

    def test_codegen(self):
        a = actual("codegen")
        self.assertEqual(set(a), HOSTS)
        for h in HOSTS:
            for l in LANGS:
                g = a[h]["jump_on_condition_line"][l]
                self.assertNotIn(None, g.values())                                   # строка условия найдена в машинном коде
                self.assertTrue(g["filter"])                                         # контроль детектора: переход находится
                self.assertFalse(g["filter_branchless"])                             # контроль: переходы цикла не засчитываются

    def test_coverage_detector(self):
        # Контроль разбора perf stat: строка с долей 1% (мультиплексирование) не теряет долю.
        import sys
        sys.path.insert(0, str(ROOT / "stand"))
        import bench
        r = bench.parse("@@perf c chase ws=1 reps=0\n1000;;cycles;10000;1.00;;\n2000;;instructions;1000000;100.00;;\n")
        self.assertEqual(r["perf_coverage"]["c chase ws=1 reps=0"], {"cycles": 1.0, "instructions": 100.0})

    def test_counters(self):
        a = actual("counters")
        self.assertEqual(set(a), LANGS)
        for row in a.values():
            self.assertEqual(row["branchless_misses_per_elem_unsorted"], "<0.05")
            self.assertEqual(row["filter_branchless_misses_per_elem_unsorted"], "<0.05")   # контроль: нет ветки — нет промахов
            self.assertIn("events_counted_full_time", row)
            self.assertNotEqual(row["chase_misses_per_access_16k"], row["chase_misses_per_access_256m"])

if __name__ == "__main__":
    unittest.main()
