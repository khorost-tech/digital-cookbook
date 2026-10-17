"""Снимок окружения: случаи, на которых проверка равенства слепнет.

Проверка сравнивает хеши, поэтому вся её сила — в том, что попадает в
снимок и что из него исключено. Слишком мягкий снимок пропустит
расхождение, слишком жёсткий забракует исправный стенд.

Отдельный случай, стоивший времени: первая попытка уронить проверку
подсовывала лишний пакет `six`, который уже был в наборе транзитивно.
Проверка справедливо ничего не заметила, и предохранитель выглядел бы
проверенным, не будучи им. Здесь такие подделки перечислены явно.
"""

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "bench"))

import installed  # noqa: E402


def make_venv(packages, python="python3.14"):
    """Каталог, похожий на окружение: только dist-info, без содержимого."""
    root = Path(tempfile.mkdtemp())
    sp = root / "lib" / python / "site-packages"
    sp.mkdir(parents=True)
    for spec in packages:
        name, _, version = spec.partition("==")
        (sp / f"{name}-{version}.dist-info").mkdir()
    return root


class TestSnapshot(unittest.TestCase):
    def test_same_set_same_hash(self):
        a = installed.snapshot(make_venv(["fastapi==0.141.1", "six==1.17.0"]))
        b = installed.snapshot(make_venv(["six==1.17.0", "fastapi==0.141.1"]))
        self.assertEqual(a["sha256"], b["sha256"])
        self.assertEqual(a["count"], 2)

    def test_missing_package_changes_hash(self):
        a = installed.snapshot(make_venv(["fastapi==0.141.1", "six==1.17.0"]))
        b = installed.snapshot(make_venv(["fastapi==0.141.1"]))
        self.assertNotEqual(a["sha256"], b["sha256"])

    def test_different_version_changes_hash(self):
        # Тот же набор имён при другой версии — расхождение, которое
        # счётчик пакетов не увидел бы.
        a = installed.snapshot(make_venv(["six==1.17.0"]))
        b = installed.snapshot(make_venv(["six==1.16.0"]))
        self.assertEqual(a["count"], b["count"])
        self.assertNotEqual(a["sha256"], b["sha256"])

    def test_bootstrap_is_excluded_but_reported(self):
        # pip приходит из СОЗДАНИЯ окружения, а не из установки: он не
        # должен ломать сравнение, но и прятать его нельзя.
        a = installed.snapshot(make_venv(["six==1.17.0"]))
        b = installed.snapshot(make_venv(["six==1.17.0", "pip==26.2.1"]))
        self.assertEqual(a["sha256"], b["sha256"])
        self.assertEqual(b["bootstrap"], ["pip==26.2.1"])
        self.assertEqual(a["bootstrap"], [])

    def test_name_case_and_separators_do_not_matter(self):
        a = installed.snapshot(make_venv(["python_dateutil==2.9.0"]))
        b = installed.snapshot(make_venv(["python-dateutil==2.9.0"]))
        self.assertEqual(a["sha256"], b["sha256"])

    def test_missing_site_packages_is_a_failure_not_an_empty_set(self):
        # Пустой набор выглядит как успешное сравнение двух пустых
        # окружений. Отсутствие site-packages обязано быть отказом.
        empty = Path(tempfile.mkdtemp())
        with self.assertRaises(FileNotFoundError):
            installed.snapshot(empty)

    def test_python_version_in_path_is_not_hardcoded(self):
        # Захардкоженный python3.14 однажды перестал бы находить
        # site-packages, а пустой список читался бы как «всё совпало».
        snap = installed.snapshot(
            make_venv(["six==1.17.0"], python="python3.15"))
        self.assertEqual(snap["count"], 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
