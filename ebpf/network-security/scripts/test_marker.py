"""Проверки маркера полного прогона и сводок сессий."""

import glob
import hashlib
import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FX = os.path.join(HERE, "fixtures")
_spec = importlib.util.spec_from_file_location("run_manifest", os.path.join(HERE, "scripts", "run-manifest.py"))
manifest = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(manifest)
_spec2 = importlib.util.spec_from_file_location("run_marker", os.path.join(HERE, "scripts", "run-marker.py"))
marker_mod = importlib.util.module_from_spec(_spec2)
_spec2.loader.exec_module(marker_mod)

PENDING = os.environ.get("STAND_MARKER_PENDING") == "1"


@unittest.skipIf(PENDING, "маркер ставится")
class Marker(unittest.TestCase):
    def setUp(self):
        with open(os.path.join(FX, "RUN-COMPLETE"), encoding="utf-8") as f:
            self.marker = json.load(f)

    def test_marker_hashes_match_fixtures(self):
        for name, digest in self.marker["fixtures"].items():
            with open(os.path.join(FX, name), "rb") as f:
                self.assertEqual(hashlib.sha256(f.read()).hexdigest(), digest, name)

    def test_marker_covers_every_fixture(self):
        self.assertEqual(sorted(self.marker["fixtures"]), sorted(marker_mod.FIXTURES))

    def test_marker_counts_are_positive(self):
        self.assertGreater(self.marker["tests"], 0)
        self.assertGreater(self.marker["sabotages"], 0)


class Manifests(unittest.TestCase):
    def test_varying_values_are_marked(self):
        # Величина, разошедшаяся между сессиями, обязана быть вне сравнения
        # («~» — помечена nondeterministic до сессий) или в оси с допуском;
        # иначе сравнение сессий её прячет — или совпадение по случаю выдаёт
        # за устойчивость.
        paths = sorted(glob.glob(os.path.join(FX, "manifests", "*.json")))
        sessions = []
        for p in paths:
            with open(p, encoding="utf-8") as f:
                sessions.append(json.load(f)["published"])
        for a, b in zip(sessions, sessions[1:]):
            diffs, _ = manifest.compare(a, b, common_only=True)
            self.assertEqual(diffs, [], "различие между соседними сессиями")

    def test_current_fixtures_match_last_session(self):
        paths = sorted(glob.glob(os.path.join(FX, "manifests", "*.json")))
        if not paths:
            self.skipTest("сводок ещё нет")
        with open(paths[-1], encoding="utf-8") as f:
            last = json.load(f)["published"]
        self.assertEqual(manifest.compare(last, manifest.published())[0], [])


if __name__ == "__main__":
    unittest.main()
