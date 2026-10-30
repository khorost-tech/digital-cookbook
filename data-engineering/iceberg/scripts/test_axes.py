"""Проверки осей стенда iceberg."""

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

    def test_lost_single_writer_is_refused(self):
        rows = [dict(r, lost_a=1) if r["variant"] == "iceberg-single" else r for r in analyze.read("writers")]
        with self.assertRaises(SystemExit):
            analyze.check("writers", rows)

    def test_reader_without_files_is_refused(self):
        rows = [dict(r, files_in_storage_mid=0) if r["variant"] == "iceberg" else r for r in analyze.read("reader")]
        with self.assertRaises(SystemExit):
            analyze.check("reader", rows)

    def test_rewritten_old_files_are_refused(self):
        rows = [dict(r, old_files_kept=0) if r["variant"] == "iceberg" else r for r in analyze.read("partitions")]
        with self.assertRaises(SystemExit):
            analyze.check("partitions", rows)


class Outcomes(unittest.TestCase):
    def test_folder_loses_first_writer(self):
        d = by("writers")
        self.assertEqual(d["folder"]["a_kept"], 0)
        self.assertEqual((d["iceberg"]["lost_a"], d["iceberg"]["lost_b"], d["iceberg"]["b_rejected"]), (0, 0, 1))

    def test_partial_read(self):
        d = by("reader")
        self.assertTrue(0 < d["folder"]["seen_mid"] < d["folder"]["new_rows"])
        self.assertEqual(d["iceberg"]["seen_mid"], 0)

    def test_delete_modes(self):
        d = by("delete")
        self.assertEqual((d["merge-on-read"]["rewritten_files"], d["merge-on-read"]["added_delete_files"]), (0, 1))
        self.assertEqual(d["copy-on-write"]["rewritten_files"], 1)
        self.assertFalse(d["folder"]["history"])
        self.assertTrue(d["copy-on-write"]["history"])

    def test_schema_by_id(self):
        d = by("schema")
        self.assertEqual((d["folder"]["renamed_old_values"], d["folder"]["readded_old_values"]),
                         (0, d["folder"]["old_rows"]))
        self.assertEqual(d["iceberg"]["outcome"], "match")

    def test_hidden_partitioning(self):
        d = by("partitions")
        self.assertEqual(d["folder"]["files_planned"], d["folder"]["files_total"])
        self.assertEqual(d["iceberg"]["files_planned"], 1)

    def test_maintenance(self):
        d = by("maintenance")
        self.assertEqual((d["write"]["data_files"], d["compact"]["data_files"], d["expire"]["snapshots"]), (50, 1, 1))
        self.assertEqual(d["expire"]["data_objects"], 1)



class Erase(unittest.TestCase):
    def test_physical_lifecycle(self):
        d = by("erase")
        cow, mor = d["copy-on-write"], d["merge-on-read"]
        # после DELETE строка физически на месте в обоих режимах
        self.assertEqual((cow["physical_after_delete"], mor["physical_after_delete"]), (1, 1))
        # copy-on-write: истечение снапшотов удаляет прежний файл
        self.assertEqual(cow["physical_after_expire"], 0)
        # merge-on-read: файл данных текущий — строка живёт до уплотнения
        self.assertEqual(mor["physical_after_expire"], 1)
        self.assertEqual(mor["physical_after_compact_expire"], 0)


if __name__ == "__main__":
    unittest.main()
