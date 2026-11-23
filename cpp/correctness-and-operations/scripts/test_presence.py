"""Проверки признаков присутствия дефекта на синтетическом дизассемблере."""

import unittest

from scripts.presence import defect_present

HEAD = "0000000000000700 <main>:\n"


class Presence(unittest.TestCase):
    def test_malloc_call_in_any_form(self):
        for target in ("malloc@plt", "malloc", "__interceptor_trampoline_malloc"):
            dis = HEAD + " 714:\tbl\t6a0 <%s>\n" % target
            self.assertTrue(defect_present("heap_overflow", dis), target)

    def test_removed_malloc_is_absent(self):
        dis = HEAD + " 7fc:\tbl\t680 <printf@plt>\n"
        self.assertFalse(defect_present("heap_overflow", dis))

    def test_similar_name_is_not_malloc(self):
        dis = HEAD + " 714:\tbl\t6a0 <malloc_usable_size@plt>\n"
        self.assertFalse(defect_present("heap_overflow", dis))

    def test_clean_has_no_defect_by_construction(self):
        self.assertIsNone(defect_present("clean", HEAD))

    def test_missing_function_is_an_error_not_absent(self):
        with self.assertRaises(ValueError):
            defect_present("parser_edge", HEAD + " 700:\tret\n")


if __name__ == "__main__":
    unittest.main()
