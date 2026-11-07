"""Проверки собранного датасета: формат, нормировка, истина, эталонная выборка."""

import array
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA = os.path.join(HERE, "data")
DIM = 384
with open(os.path.join(HERE, "dataset.json"), encoding="utf-8") as f:
    SPEC = json.load(f)


def load(path, typ="f"):
    a = array.array(typ)
    with open(path, "rb") as f:
        a.frombytes(f.read())
    return a


def vec(a, i):
    return a[i * DIM:(i + 1) * DIM]


def dot(u, v):
    return sum(x * y for x, y in zip(u, v))


class Dataset(unittest.TestCase):
    def each(self):
        for name, spec in SPEC["corpora"].items():
            yield name, spec, os.path.join(DATA, name)

    def test_shapes_and_ids(self):
        for name, _, d in self.each():
            for part in ("corpus", "queries"):
                size = os.path.getsize(os.path.join(d, part + ".f32"))
                self.assertEqual(size % (4 * DIM), 0, name + "/" + part)
                with open(os.path.join(d, part + ".ids"), encoding="utf-8") as f:
                    ids = f.read().split("\n")[:-1]
                self.assertEqual(len(ids), size // 4 // DIM, name + "/" + part)
                self.assertEqual(len(set(ids)), len(ids), name + "/" + part + ": повторы id")

    def test_normalized(self):
        for name, _, d in self.each():
            c = load(os.path.join(d, "corpus.f32"))
            n = len(c) // DIM
            for i in range(0, n, max(1, n // 500)):
                self.assertAlmostEqual(dot(vec(c, i), vec(c, i)), 1.0, places=4, msg=name)

    def test_ground_truth(self):
        """Скоры истины по убыванию и равны пересчитанному скалярному произведению."""
        for name, spec, d in self.each():
            k = spec["ground_truth"]["k"]
            c, q = load(os.path.join(d, "corpus.f32")), load(os.path.join(d, "queries.f32"))
            gi, gf = load(os.path.join(d, "gt.i32"), "i"), load(os.path.join(d, "gt.f32"))
            with open(os.path.join(d, "gt.rows")) as f:
                rows = [int(x) for x in f]
            self.assertEqual(len(gi), len(rows) * k, name)
            for n, r in enumerate(rows[::50]):
                n *= 50
                scores = gf[n * k:(n + 1) * k]
                self.assertEqual(list(scores), sorted(scores, reverse=True), name)
                for j in (0, k - 1):
                    self.assertAlmostEqual(dot(vec(q, r), vec(c, gi[n * k + j])), scores[j], places=4, msg=name)


if __name__ == "__main__":
    unittest.main()
